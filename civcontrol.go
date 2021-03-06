package main

import (
	"fmt"
	"math"
	"sync"
	"time"
)

const statusPollInterval = time.Second
const commandRetryTimeout = 500 * time.Millisecond
const pttTimeout = 3 * time.Minute
const tuneTimeout = 30 * time.Second

// Commands reference: https://www.icomeurope.com/wp-content/uploads/2020/08/IC-705_ENG_CI-V_1_20200721.pdf

type civOperatingMode struct {
	name string
	code byte
}

var civOperatingModes = []civOperatingMode{
	{name: "LSB", code: 0x00},
	{name: "USB", code: 0x01},
	{name: "AM", code: 0x02},
	{name: "CW", code: 0x03},
	{name: "RTTY", code: 0x04},
	{name: "FM", code: 0x05},
	{name: "WFM", code: 0x06},
	{name: "CW-R", code: 0x07},
	{name: "RTTY-R", code: 0x08},
	{name: "DV", code: 0x17},
}

type civFilter struct {
	name string
	code byte
}

var civFilters = []civFilter{
	{name: "FIL1", code: 0x01},
	{name: "FIL2", code: 0x02},
	{name: "FIL3", code: 0x03},
}

type civBand struct {
	freqFrom uint
	freqTo   uint
	freq     uint
}

var civBands = []civBand{
	{freqFrom: 1800000, freqTo: 1999999},     // 1.9
	{freqFrom: 3400000, freqTo: 4099999},     // 3.5
	{freqFrom: 6900000, freqTo: 7499999},     // 7
	{freqFrom: 9900000, freqTo: 10499999},    // 10
	{freqFrom: 13900000, freqTo: 14499999},   // 14
	{freqFrom: 17900000, freqTo: 18499999},   // 18
	{freqFrom: 20900000, freqTo: 21499999},   // 21
	{freqFrom: 24400000, freqTo: 25099999},   // 24
	{freqFrom: 28000000, freqTo: 29999999},   // 28
	{freqFrom: 50000000, freqTo: 54000000},   // 50
	{freqFrom: 74800000, freqTo: 107999999},  // WFM
	{freqFrom: 108000000, freqTo: 136999999}, // AIR
	{freqFrom: 144000000, freqTo: 148000000}, // 144
	{freqFrom: 420000000, freqTo: 450000000}, // 430
	{freqFrom: 0, freqTo: 0},                 // GENE
}

type splitMode int

const (
	splitModeOff = iota
	splitModeOn
	splitModeDUPMinus
	splitModeDUPPlus
)

type civCmd struct {
	pending bool
	sentAt  time.Time
	name    string
	cmd     []byte
}

type civControlStruct struct {
	st                 *serialStream
	deinitNeeded       chan bool
	deinitFinished     chan bool
	resetSReadTimer    chan bool
	newPendingCmdAdded chan bool

	state struct {
		mutex       sync.Mutex
		pendingCmds []*civCmd

		getPwr            civCmd
		getS              civCmd
		getOVF            civCmd
		getSWR            civCmd
		getTransmitStatus civCmd
		getPreamp         civCmd
		getAGC            civCmd
		getTuneStatus     civCmd
		getVd             civCmd
		getTS             civCmd
		getRFGain         civCmd
		getSQL            civCmd
		getNR             civCmd
		getNREnabled      civCmd
		getSplit          civCmd
		getMainVFOFreq    civCmd
		getSubVFOFreq     civCmd
		getMainVFOMode    civCmd
		getSubVFOMode     civCmd

		lastSReceivedAt       time.Time
		lastOVFReceivedAt     time.Time
		lastSWRReceivedAt     time.Time
		lastVFOFreqReceivedAt time.Time

		setPwr         civCmd
		setRFGain      civCmd
		setSQL         civCmd
		setNR          civCmd
		setMainVFOFreq civCmd
		setSubVFOFreq  civCmd
		setMode        civCmd
		setSubVFOMode  civCmd
		setPTT         civCmd
		setTune        civCmd
		setDataMode    civCmd
		setPreamp      civCmd
		setAGC         civCmd
		setNREnabled   civCmd
		setTS          civCmd
		setVFO         civCmd
		setSplit       civCmd

		pttTimeoutTimer  *time.Timer
		tuneTimeoutTimer *time.Timer

		freq                uint
		subFreq             uint
		ptt                 bool
		tune                bool
		pwrPercent          int
		rfGainPercent       int
		sqlPercent          int
		nrPercent           int
		nrEnabled           bool
		operatingModeIdx    int
		dataMode            bool
		filterIdx           int
		subOperatingModeIdx int
		subDataMode         bool
		subFilterIdx        int
		bandIdx             int
		preamp              int
		agc                 int
		tsValue             byte
		ts                  uint
		vfoBActive          bool
		splitMode           splitMode
	}
}

var civControl civControlStruct

// Returns false if the message should not be forwarded to the serial port TCP server or the virtual serial port.
func (s *civControlStruct) decode(d []byte) bool {
	if len(d) < 6 || d[0] != 0xfe || d[1] != 0xfe || d[len(d)-1] != 0xfd {
		return true
	}

	payload := d[5 : len(d)-1]

	s.state.mutex.Lock()
	defer s.state.mutex.Unlock()

	switch d[4] {
	// case 0x00:
	// 	return s.decodeFreq(payload)
	case 0x01:
		return s.decodeMode(payload)
	// case 0x03:
	// 	return s.decodeFreq(payload)
	case 0x04:
		return s.decodeMode(payload)
	// case 0x05:
	// 	return s.decodeFreq(payload)
	case 0x06:
		return s.decodeMode(payload)
	case 0x07:
		return s.decodeVFO(payload)
	case 0x0f:
		return s.decodeSplit(payload)
	case 0x10:
		return s.decodeTS(payload)
	case 0x1a:
		return s.decodeDataModeAndOVF(payload)
	case 0x14:
		return s.decodePowerRFGainSQLNRPwr(payload)
	case 0x1c:
		return s.decodeTransmitStatus(payload)
	case 0x15:
		return s.decodeVdSWRS(payload)
	case 0x16:
		return s.decodePreampAGCNREnabled(payload)
	case 0x25:
		return s.decodeVFOFreq(payload)
	case 0x26:
		return s.decodeVFOMode(payload)
	}
	return true
}

func (s *civControlStruct) decodeFreqData(d []byte) (f uint) {
	var pos int
	for _, v := range d {
		s1 := v & 0x0f
		s2 := v >> 4
		f += uint(s1) * uint(math.Pow(10, float64(pos)))
		pos++
		f += uint(s2) * uint(math.Pow(10, float64(pos)))
		pos++
	}
	return
}

// func (s *civControlStruct) decodeFreq(d []byte) bool {
// 	if len(d) < 2 {
// 		return !s.state.getFreq.pending && !s.state.setMainVFOFreq.pending
// 	}

// 	s.state.freq = s.decodeFreqData(d)
// 	statusLog.reportFrequency(s.state.freq)

// 	s.state.bandIdx = len(civBands) - 1 // Set the band idx to GENE by default.
// 	for i := range civBands {
// 		if s.state.freq >= civBands[i].freqFrom && s.state.freq <= civBands[i].freqTo {
// 			s.state.bandIdx = i
// 			civBands[s.state.bandIdx].freq = s.state.freq
// 			break
// 		}
// 	}

// 	if s.state.getFreq.pending {
// 		s.removePendingCmd(&s.state.getFreq)
// 		return false
// 	}
// 	if s.state.setMainVFOFreq.pending {
// 		s.removePendingCmd(&s.state.setMainVFOFreq)
// 		return false
// 	}
// 	return true
// }

func (s *civControlStruct) decodeFilterValueToFilterIdx(v byte) int {
	for i := range civFilters {
		if civFilters[i].code == v {
			return i
		}
	}
	return 0
}

func (s *civControlStruct) decodeMode(d []byte) bool {
	if len(d) < 1 {
		return !s.state.setMode.pending
	}

	for i := range civOperatingModes {
		if civOperatingModes[i].code == d[0] {
			s.state.operatingModeIdx = i
			break
		}
	}

	if len(d) > 1 {
		s.state.filterIdx = s.decodeFilterValueToFilterIdx(d[1])
	}
	statusLog.reportMode(civOperatingModes[s.state.operatingModeIdx].name, s.state.dataMode,
		civFilters[s.state.filterIdx].name)

	if s.state.setMode.pending {
		s.removePendingCmd(&s.state.setMode)
		return false
	}
	return true
}

func (s *civControlStruct) decodeVFO(d []byte) bool {
	if len(d) < 1 {
		return !s.state.setVFO.pending
	}

	if d[0] == 1 {
		s.state.vfoBActive = true
		log.Print("active vfo: B")
	} else {
		s.state.vfoBActive = false
		log.Print("active vfo: A")
	}

	if s.state.setVFO.pending {
		// The radio does not send frequencies automatically.
		_ = s.getBothVFOFreq()
		s.removePendingCmd(&s.state.setVFO)
		return false
	}
	return true
}

func (s *civControlStruct) decodeSplit(d []byte) bool {
	if len(d) < 1 {
		return !s.state.getSplit.pending && !s.state.setSplit.pending
	}

	var str string
	switch d[0] {
	default:
		s.state.splitMode = splitModeOff
	case 0x01:
		s.state.splitMode = splitModeOn
		str = "SPLIT"
	case 0x11:
		s.state.splitMode = splitModeDUPMinus
		str = "DUP-"
	case 0x12:
		s.state.splitMode = splitModeDUPPlus
		str = "DUP+"
	}
	statusLog.reportSplit(s.state.splitMode, str)

	if s.state.getSplit.pending {
		s.removePendingCmd(&s.state.getSplit)
		return false
	}
	if s.state.setSplit.pending {
		s.removePendingCmd(&s.state.setSplit)
		return false
	}
	return true
}

func (s *civControlStruct) decodeTS(d []byte) bool {
	if len(d) < 1 {
		return !s.state.getTS.pending && !s.state.setTS.pending
	}

	s.state.tsValue = d[0]

	switch s.state.tsValue {
	default:
		s.state.ts = 1
	case 1:
		s.state.ts = 100
	case 2:
		s.state.ts = 500
	case 3:
		s.state.ts = 1000
	case 4:
		s.state.ts = 5000
	case 5:
		s.state.ts = 6250
	case 6:
		s.state.ts = 8330
	case 7:
		s.state.ts = 9000
	case 8:
		s.state.ts = 10000
	case 9:
		s.state.ts = 12500
	case 10:
		s.state.ts = 20000
	case 11:
		s.state.ts = 25000
	case 12:
		s.state.ts = 50000
	case 13:
		s.state.ts = 100000
	}
	statusLog.reportTS(s.state.ts)

	if s.state.getTS.pending {
		s.removePendingCmd(&s.state.getTS)
		return false
	}
	if s.state.setTS.pending {
		s.removePendingCmd(&s.state.setTS)
		return false
	}
	return true
}

func (s *civControlStruct) decodeDataModeAndOVF(d []byte) bool {
	switch d[0] {
	case 0x06:
		if len(d) < 3 {
			return !s.state.setDataMode.pending
		}
		if d[1] == 1 {
			s.state.dataMode = true
			s.state.filterIdx = s.decodeFilterValueToFilterIdx(d[2])
		} else {
			s.state.dataMode = false
		}

		statusLog.reportMode(civOperatingModes[s.state.operatingModeIdx].name, s.state.dataMode,
			civFilters[s.state.filterIdx].name)

		if s.state.setDataMode.pending {
			s.removePendingCmd(&s.state.setDataMode)
			return false
		}
	case 0x09:
		if len(d) < 2 {
			return !s.state.getOVF.pending
		}
		if d[1] != 0 {
			statusLog.reportOVF(true)
		} else {
			statusLog.reportOVF(false)
		}
		s.state.lastOVFReceivedAt = time.Now()
		if s.state.getOVF.pending {
			s.removePendingCmd(&s.state.getOVF)
			return false
		}
	}
	return true
}

func (s *civControlStruct) decodePowerRFGainSQLNRPwr(d []byte) bool {
	switch d[0] {
	case 0x02:
		if len(d) < 3 {
			return !s.state.getRFGain.pending && !s.state.setRFGain.pending
		}
		hex := uint16(d[1])<<8 | uint16(d[2])
		s.state.rfGainPercent = int(math.Round((float64(hex) / 0x0255) * 100))
		statusLog.reportRFGain(s.state.rfGainPercent)
		if s.state.getRFGain.pending {
			s.removePendingCmd(&s.state.getRFGain)
			return false
		}
		if s.state.setRFGain.pending {
			s.removePendingCmd(&s.state.setRFGain)
			return false
		}
	case 0x03:
		if len(d) < 3 {
			return !s.state.getSQL.pending && !s.state.setSQL.pending
		}
		hex := uint16(d[1])<<8 | uint16(d[2])
		s.state.sqlPercent = int(math.Round((float64(hex) / 0x0255) * 100))
		statusLog.reportSQL(s.state.sqlPercent)
		if s.state.getSQL.pending {
			s.removePendingCmd(&s.state.getSQL)
			return false
		}
		if s.state.setSQL.pending {
			s.removePendingCmd(&s.state.setSQL)
			return false
		}
	case 0x06:
		if len(d) < 3 {
			return !s.state.getNR.pending && !s.state.setNR.pending
		}
		hex := uint16(d[1])<<8 | uint16(d[2])
		s.state.nrPercent = int(math.Round((float64(hex) / 0x0255) * 100))
		statusLog.reportNR(s.state.nrPercent)
		if s.state.getNR.pending {
			s.removePendingCmd(&s.state.getNR)
			return false
		}
		if s.state.setNR.pending {
			s.removePendingCmd(&s.state.setNR)
			return false
		}
	case 0x0a:
		if len(d) < 3 {
			return !s.state.getPwr.pending && !s.state.setPwr.pending
		}
		hex := uint16(d[1])<<8 | uint16(d[2])
		s.state.pwrPercent = int(math.Round((float64(hex) / 0x0255) * 100))
		statusLog.reportTxPower(s.state.pwrPercent)
		if s.state.getPwr.pending {
			s.removePendingCmd(&s.state.getPwr)
			return false
		}
		if s.state.setPwr.pending {
			s.removePendingCmd(&s.state.setPwr)
			return false
		}
	}
	return true
}

func (s *civControlStruct) decodeTransmitStatus(d []byte) bool {
	if len(d) < 2 {
		return !s.state.getTuneStatus.pending && !s.state.getTransmitStatus.pending && !s.state.setPTT.pending
	}

	switch d[0] {
	case 0:
		if d[1] == 1 {
			s.state.ptt = true
		} else {
			if s.state.ptt { // PTT released?
				s.state.ptt = false
				if s.state.pttTimeoutTimer != nil {
					s.state.pttTimeoutTimer.Stop()
				}
				_ = s.getVd()
			}
		}
		statusLog.reportPTT(s.state.ptt, s.state.tune)
		if s.state.setPTT.pending {
			s.removePendingCmd(&s.state.setPTT)
			return false
		}
	case 1:
		if d[1] == 2 {
			s.state.tune = true

			// The transceiver does not send the tune state after it's finished.
			time.AfterFunc(time.Second, func() {
				_ = s.getTransmitStatus()
			})
		} else {
			if s.state.tune { // Tune finished?
				s.state.tune = false
				s.state.tuneTimeoutTimer.Stop()
				_ = s.getVd()
			}
		}

		statusLog.reportPTT(s.state.ptt, s.state.tune)
		if s.state.setTune.pending {
			s.removePendingCmd(&s.state.setTune)
			return false
		}
	}

	if s.state.getTuneStatus.pending {
		s.removePendingCmd(&s.state.getTuneStatus)
		return false
	}
	if s.state.getTransmitStatus.pending {
		s.removePendingCmd(&s.state.getTransmitStatus)
		return false
	}
	return true
}

func (s *civControlStruct) decodeVdSWRS(d []byte) bool {
	switch d[0] {
	case 0x02:
		if len(d) < 3 {
			return !s.state.getS.pending
		}
		sValue := (int(math.Round(((float64(int(d[1])<<8) + float64(d[2])) / 0x0241) * 18)))
		sStr := "S"
		if sValue <= 9 {
			sStr += fmt.Sprint(sValue)
		} else {
			sStr += "9+"

			switch sValue {
			case 10:
				sStr += "10"
			case 11:
				sStr += "20"
			case 12:
				sStr += "30"
			case 13:
				sStr += "40"
			case 14:
				sStr += "40"
			case 15:
				sStr += "40"
			case 16:
				sStr += "40"
			case 17:
				sStr += "50"
			case 18:
				sStr += "50"
			default:
				sStr += "60"
			}
		}
		s.state.lastSReceivedAt = time.Now()
		statusLog.reportS(sStr)
		if s.state.getS.pending {
			s.removePendingCmd(&s.state.getS)
			return false
		}
	case 0x12:
		if len(d) < 3 {
			return !s.state.getSWR.pending
		}
		s.state.lastSWRReceivedAt = time.Now()
		statusLog.reportSWR(((float64(int(d[1])<<8)+float64(d[2]))/0x0120)*2 + 1)
		if s.state.getSWR.pending {
			s.removePendingCmd(&s.state.getSWR)
			return false
		}
	case 0x15:
		if len(d) < 3 {
			return !s.state.getVd.pending
		}
		statusLog.reportVd(((float64(int(d[1])<<8) + float64(d[2])) / 0x0241) * 16)
		if s.state.getVd.pending {
			s.removePendingCmd(&s.state.getVd)
			return false
		}
	}
	return true
}

func (s *civControlStruct) decodePreampAGCNREnabled(d []byte) bool {
	switch d[0] {
	case 0x02:
		if len(d) < 2 {
			return !s.state.getPreamp.pending && !s.state.setPreamp.pending
		}
		s.state.preamp = int(d[1])
		statusLog.reportPreamp(s.state.preamp)
		if s.state.getPreamp.pending {
			s.removePendingCmd(&s.state.getPreamp)
			return false
		}
		if s.state.setPreamp.pending {
			s.removePendingCmd(&s.state.setPreamp)
			return false
		}
	case 0x12:
		if len(d) < 2 {
			return !s.state.getAGC.pending && !s.state.setAGC.pending
		}
		s.state.agc = int(d[1])
		var agc string
		switch s.state.agc {
		case 1:
			agc = "F"
		case 2:
			agc = "M"
		case 3:
			agc = "S"
		}
		statusLog.reportAGC(agc)
		if s.state.getAGC.pending {
			s.removePendingCmd(&s.state.getAGC)
			return false
		}
		if s.state.setAGC.pending {
			s.removePendingCmd(&s.state.setAGC)
			return false
		}
	case 0x40:
		if len(d) < 2 {
			return !s.state.getNREnabled.pending && !s.state.setNREnabled.pending
		}
		if d[1] == 1 {
			s.state.nrEnabled = true
		} else {
			s.state.nrEnabled = false
		}
		statusLog.reportNREnabled(s.state.nrEnabled)
		if s.state.getNREnabled.pending {
			s.removePendingCmd(&s.state.getNREnabled)
			return false
		}
		if s.state.setNREnabled.pending {
			s.removePendingCmd(&s.state.setNREnabled)
			return false
		}
	}
	return true
}

func (s *civControlStruct) decodeVFOFreq(d []byte) bool {
	if len(d) < 2 {
		return !s.state.getMainVFOFreq.pending && !s.state.getSubVFOFreq.pending && !s.state.setSubVFOFreq.pending
	}

	f := s.decodeFreqData(d[1:])
	switch d[0] {
	default:
		s.state.freq = f
		statusLog.reportFrequency(s.state.freq)

		s.state.bandIdx = len(civBands) - 1 // Set the band idx to GENE by default.
		for i := range civBands {
			if s.state.freq >= civBands[i].freqFrom && s.state.freq <= civBands[i].freqTo {
				s.state.bandIdx = i
				civBands[s.state.bandIdx].freq = s.state.freq
				break
			}
		}

		if s.state.getMainVFOFreq.pending {
			s.removePendingCmd(&s.state.getMainVFOFreq)
			return false
		}
		if s.state.setMainVFOFreq.pending {
			s.removePendingCmd(&s.state.setMainVFOFreq)
			return false
		}
	case 0x01:
		s.state.subFreq = f
		statusLog.reportSubFrequency(s.state.subFreq)
		if s.state.getSubVFOFreq.pending {
			s.removePendingCmd(&s.state.getSubVFOFreq)
			return false
		}
		if s.state.setSubVFOFreq.pending {
			s.removePendingCmd(&s.state.setSubVFOFreq)
			return false
		}
	}
	return true
}

func (s *civControlStruct) decodeVFOMode(d []byte) bool {
	if len(d) < 2 {
		return !s.state.getMainVFOMode.pending && !s.state.getSubVFOMode.pending && !s.state.setSubVFOMode.pending
	}

	operatingModeIdx := -1
	for i := range civOperatingModes {
		if civOperatingModes[i].code == d[1] {
			operatingModeIdx = i
			break
		}
	}
	var dataMode bool
	if len(d) > 2 && d[2] != 0 {
		dataMode = true
	}
	filterIdx := -1
	if len(d) > 3 {
		filterIdx = s.decodeFilterValueToFilterIdx(d[3])
	}

	switch d[0] {
	default:
		s.state.operatingModeIdx = operatingModeIdx
		s.state.dataMode = dataMode
		if filterIdx >= 0 {
			s.state.filterIdx = filterIdx
		}
		statusLog.reportMode(civOperatingModes[s.state.operatingModeIdx].name, s.state.dataMode,
			civFilters[s.state.filterIdx].name)

		if s.state.getMainVFOMode.pending {
			s.removePendingCmd(&s.state.getMainVFOMode)
			return false
		}
	case 0x01:
		s.state.subOperatingModeIdx = operatingModeIdx
		s.state.subDataMode = dataMode
		s.state.subFilterIdx = filterIdx
		statusLog.reportSubMode(civOperatingModes[s.state.subOperatingModeIdx].name, s.state.subDataMode,
			civFilters[s.state.subFilterIdx].name)

		if s.state.getSubVFOMode.pending {
			s.removePendingCmd(&s.state.getSubVFOMode)
			return false
		}
		if s.state.setSubVFOMode.pending {
			s.removePendingCmd(&s.state.setSubVFOMode)
			return false
		}
	}
	return true
}

func (s *civControlStruct) initCmd(cmd *civCmd, name string, data []byte) {
	*cmd = civCmd{}
	cmd.name = name
	cmd.cmd = data
}

func (s *civControlStruct) getPendingCmdIndex(cmd *civCmd) int {
	for i := range s.state.pendingCmds {
		if cmd == s.state.pendingCmds[i] {
			return i
		}
	}
	return -1
}

func (s *civControlStruct) removePendingCmd(cmd *civCmd) {
	cmd.pending = false
	index := s.getPendingCmdIndex(cmd)
	if index < 0 {
		return
	}
	s.state.pendingCmds[index] = s.state.pendingCmds[len(s.state.pendingCmds)-1]
	s.state.pendingCmds[len(s.state.pendingCmds)-1] = nil
	s.state.pendingCmds = s.state.pendingCmds[:len(s.state.pendingCmds)-1]
}

func (s *civControlStruct) sendCmd(cmd *civCmd) error {
	if s.st == nil {
		return nil
	}

	cmd.pending = true
	cmd.sentAt = time.Now()
	if s.getPendingCmdIndex(cmd) < 0 {
		s.state.pendingCmds = append(s.state.pendingCmds, cmd)
		select {
		case s.newPendingCmdAdded <- true:
		default:
		}
	}
	return s.st.send(cmd.cmd)
}

func (s *civControlStruct) setPwr(percent int) error {
	v := uint16(0x0255 * (float64(percent) / 100))
	s.initCmd(&s.state.setPwr, "setPwr", []byte{254, 254, civAddress, 224, 0x14, 0x0a, byte(v >> 8), byte(v & 0xff), 253})
	return s.sendCmd(&s.state.setPwr)
}

func (s *civControlStruct) incPwr() error {
	if s.state.pwrPercent < 100 {
		return s.setPwr(s.state.pwrPercent + 1)
	}
	return nil
}

func (s *civControlStruct) decPwr() error {
	if s.state.pwrPercent > 0 {
		return s.setPwr(s.state.pwrPercent - 1)
	}
	return nil
}

func (s *civControlStruct) setRFGain(percent int) error {
	v := uint16(0x0255 * (float64(percent) / 100))
	s.initCmd(&s.state.setRFGain, "setRFGain", []byte{254, 254, civAddress, 224, 0x14, 0x02, byte(v >> 8), byte(v & 0xff), 253})
	return s.sendCmd(&s.state.setRFGain)
}

func (s *civControlStruct) incRFGain() error {
	if s.state.rfGainPercent < 100 {
		return s.setRFGain(s.state.rfGainPercent + 1)
	}
	return nil
}

func (s *civControlStruct) decRFGain() error {
	if s.state.rfGainPercent > 0 {
		return s.setRFGain(s.state.rfGainPercent - 1)
	}
	return nil
}

func (s *civControlStruct) setSQL(percent int) error {
	v := uint16(0x0255 * (float64(percent) / 100))
	s.initCmd(&s.state.setSQL, "setSQL", []byte{254, 254, civAddress, 224, 0x14, 0x03, byte(v >> 8), byte(v & 0xff), 253})
	return s.sendCmd(&s.state.setSQL)
}

func (s *civControlStruct) incSQL() error {
	if s.state.sqlPercent < 100 {
		return s.setSQL(s.state.sqlPercent + 1)
	}
	return nil
}

func (s *civControlStruct) decSQL() error {
	if s.state.sqlPercent > 0 {
		return s.setSQL(s.state.sqlPercent - 1)
	}
	return nil
}

func (s *civControlStruct) setNR(percent int) error {
	if !s.state.nrEnabled {
		if err := s.toggleNR(); err != nil {
			return err
		}
	}
	v := uint16(0x0255 * (float64(percent) / 100))
	s.initCmd(&s.state.setNR, "setNR", []byte{254, 254, civAddress, 224, 0x14, 0x06, byte(v >> 8), byte(v & 0xff), 253})
	return s.sendCmd(&s.state.setNR)
}

func (s *civControlStruct) incNR() error {
	if s.state.nrPercent < 100 {
		return s.setNR(s.state.nrPercent + 1)
	}
	return nil
}

func (s *civControlStruct) decNR() error {
	if s.state.nrPercent > 0 {
		return s.setNR(s.state.nrPercent - 1)
	}
	return nil
}

func (s *civControlStruct) getDigit(v uint, n int) byte {
	f := float64(v)
	for n > 0 {
		f /= 10
		n--
	}
	return byte(uint(f) % 10)
}

func (s *civControlStruct) incFreq() error {
	return s.setMainVFOFreq(s.state.freq + s.state.ts)
}

func (s *civControlStruct) decFreq() error {
	return s.setMainVFOFreq(s.state.freq - s.state.ts)
}

func (s *civControlStruct) encodeFreqData(f uint) (b [5]byte) {
	v0 := s.getDigit(f, 9)
	v1 := s.getDigit(f, 8)
	b[4] = v0<<4 | v1
	v0 = s.getDigit(f, 7)
	v1 = s.getDigit(f, 6)
	b[3] = v0<<4 | v1
	v0 = s.getDigit(f, 5)
	v1 = s.getDigit(f, 4)
	b[2] = v0<<4 | v1
	v0 = s.getDigit(f, 3)
	v1 = s.getDigit(f, 2)
	b[1] = v0<<4 | v1
	v0 = s.getDigit(f, 1)
	v1 = s.getDigit(f, 0)
	b[0] = v0<<4 | v1
	return
}

func (s *civControlStruct) setMainVFOFreq(f uint) error {
	b := s.encodeFreqData(f)
	s.initCmd(&s.state.setMainVFOFreq, "setMainVFOFreq", []byte{254, 254, civAddress, 224, 0x25, 0x00, b[0], b[1], b[2], b[3], b[4], 253})
	return s.sendCmd(&s.state.setMainVFOFreq)
}

func (s *civControlStruct) setSubVFOFreq(f uint) error {
	b := s.encodeFreqData(f)
	s.initCmd(&s.state.setSubVFOFreq, "setSubVFOFreq", []byte{254, 254, civAddress, 224, 0x25, 0x01, b[0], b[1], b[2], b[3], b[4], 253})
	return s.sendCmd(&s.state.setSubVFOFreq)
}

func (s *civControlStruct) incOperatingMode() error {
	s.state.operatingModeIdx++
	if s.state.operatingModeIdx >= len(civOperatingModes) {
		s.state.operatingModeIdx = 0
	}
	return civControl.setOperatingModeAndFilter(civOperatingModes[s.state.operatingModeIdx].code,
		civFilters[s.state.filterIdx].code)
}

func (s *civControlStruct) decOperatingMode() error {
	s.state.operatingModeIdx--
	if s.state.operatingModeIdx < 0 {
		s.state.operatingModeIdx = len(civOperatingModes) - 1
	}
	return civControl.setOperatingModeAndFilter(civOperatingModes[s.state.operatingModeIdx].code,
		civFilters[s.state.filterIdx].code)
}

func (s *civControlStruct) incFilter() error {
	s.state.filterIdx++
	if s.state.filterIdx >= len(civFilters) {
		s.state.filterIdx = 0
	}
	return civControl.setOperatingModeAndFilter(civOperatingModes[s.state.operatingModeIdx].code,
		civFilters[s.state.filterIdx].code)
}

func (s *civControlStruct) decFilter() error {
	s.state.filterIdx--
	if s.state.filterIdx < 0 {
		s.state.filterIdx = len(civFilters) - 1
	}
	return civControl.setOperatingModeAndFilter(civOperatingModes[s.state.operatingModeIdx].code,
		civFilters[s.state.filterIdx].code)
}

func (s *civControlStruct) setOperatingModeAndFilter(modeCode, filterCode byte) error {
	s.initCmd(&s.state.setMode, "setMode", []byte{254, 254, civAddress, 224, 0x06, modeCode, filterCode, 253})
	if err := s.sendCmd(&s.state.setMode); err != nil {
		return err
	}
	return s.getBothVFOMode()
}

func (s *civControlStruct) setSubVFOMode(modeCode, dataMode, filterCode byte) error {
	s.initCmd(&s.state.setSubVFOMode, "setSubVFOMode", []byte{254, 254, civAddress, 224, 0x26, 0x01, modeCode, dataMode, filterCode, 253})
	return s.sendCmd(&s.state.setSubVFOMode)
}

func (s *civControlStruct) setPTT(enable bool) error {
	var b byte
	if enable {
		b = 1
		s.state.pttTimeoutTimer = time.AfterFunc(pttTimeout, func() {
			_ = s.setPTT(false)
		})
	}
	s.initCmd(&s.state.setPTT, "setPTT", []byte{254, 254, civAddress, 224, 0x1c, 0, b, 253})
	return s.sendCmd(&s.state.setPTT)
}

func (s *civControlStruct) setTune(enable bool) error {
	if s.state.ptt {
		return nil
	}

	var b byte
	if enable {
		b = 2
		s.state.tuneTimeoutTimer = time.AfterFunc(tuneTimeout, func() {
			_ = s.setTune(false)
		})
	} else {
		b = 1
	}
	s.initCmd(&s.state.setTune, "setTune", []byte{254, 254, civAddress, 224, 0x1c, 1, b, 253})
	return s.sendCmd(&s.state.setTune)
}

func (s *civControlStruct) toggleTune() error {
	return s.setTune(!s.state.tune)
}

func (s *civControlStruct) setDataMode(enable bool) error {
	var b byte
	var f byte
	if enable {
		b = 1
		f = 1
	} else {
		b = 0
		f = 0
	}
	s.initCmd(&s.state.setDataMode, "setDataMode", []byte{254, 254, civAddress, 224, 0x1a, 0x06, b, f, 253})
	return s.sendCmd(&s.state.setDataMode)
}

func (s *civControlStruct) toggleDataMode() error {
	return s.setDataMode(!s.state.dataMode)
}

func (s *civControlStruct) incBand() error {
	i := s.state.bandIdx + 1
	if i >= len(civBands) {
		i = 0
	}
	f := civBands[i].freq
	if f == 0 {
		f = (civBands[i].freqFrom + civBands[i].freqTo) / 2
	}
	return s.setMainVFOFreq(f)
}

func (s *civControlStruct) decBand() error {
	i := s.state.bandIdx - 1
	if i < 0 {
		i = len(civBands) - 1
	}
	f := civBands[i].freq
	if f == 0 {
		f = civBands[i].freqFrom
	}
	return s.setMainVFOFreq(f)
}

func (s *civControlStruct) togglePreamp() error {
	b := byte(s.state.preamp + 1)
	if b > 2 {
		b = 0
	}
	s.initCmd(&s.state.setPreamp, "setPreamp", []byte{254, 254, civAddress, 224, 0x16, 0x02, b, 253})
	return s.sendCmd(&s.state.setPreamp)
}

func (s *civControlStruct) toggleAGC() error {
	b := byte(s.state.agc + 1)
	if b > 3 {
		b = 1
	}
	s.initCmd(&s.state.setAGC, "setAGC", []byte{254, 254, civAddress, 224, 0x16, 0x12, b, 253})
	return s.sendCmd(&s.state.setAGC)
}

func (s *civControlStruct) toggleNR() error {
	var b byte
	if !s.state.nrEnabled {
		b = 1
	}
	s.initCmd(&s.state.setNREnabled, "setNREnabled", []byte{254, 254, civAddress, 224, 0x16, 0x40, b, 253})
	return s.sendCmd(&s.state.setNREnabled)
}

func (s *civControlStruct) setTS(b byte) error {
	s.initCmd(&s.state.setTS, "setTS", []byte{254, 254, civAddress, 224, 0x10, b, 253})
	return s.sendCmd(&s.state.setTS)
}

func (s *civControlStruct) incTS() error {
	var b byte
	if s.state.tsValue == 13 {
		b = 0
	} else {
		b = s.state.tsValue + 1
	}
	return s.setTS(b)
}

func (s *civControlStruct) decTS() error {
	var b byte
	if s.state.tsValue == 0 {
		b = 13
	} else {
		b = s.state.tsValue - 1
	}
	return s.setTS(b)
}

func (s *civControlStruct) setVFO(nr byte) error {
	s.initCmd(&s.state.setVFO, "setVFO", []byte{254, 254, civAddress, 224, 0x07, nr, 253})
	if err := s.sendCmd(&s.state.setVFO); err != nil {
		return err
	}
	return s.getBothVFOMode()
}

func (s *civControlStruct) toggleVFO() error {
	var b byte
	if !s.state.vfoBActive {
		b = 1
	}
	return s.setVFO(b)
}

func (s *civControlStruct) setSplit(mode splitMode) error {
	var b byte
	switch mode {
	default:
		b = 0x10
	case splitModeOn:
		b = 0x01
	case splitModeDUPMinus:
		b = 0x11
	case splitModeDUPPlus:
		b = 0x12
	}
	s.initCmd(&s.state.setSplit, "setSplit", []byte{254, 254, civAddress, 224, 0x0f, b, 253})
	return s.sendCmd(&s.state.setSplit)
}

func (s *civControlStruct) toggleSplit() error {
	var mode splitMode
	switch s.state.splitMode {
	case splitModeOff:
		mode = splitModeOn
	case splitModeOn:
		mode = splitModeDUPMinus
	case splitModeDUPMinus:
		mode = splitModeDUPPlus
	default:
		mode = splitModeOff
	}
	return s.setSplit(mode)
}

// func (s *civControlStruct) getFreq() error {
// 	s.initCmd(&s.state.getFreq, "getFreq", []byte{254, 254, civAddress, 224, 3, 253})
// 	return s.sendCmd(&s.state.getFreq)
// }

// func (s *civControlStruct) getMode() error {
// 	s.initCmd(&s.state.getMode, "getMode", []byte{254, 254, civAddress, 224, 4, 253})
// 	return s.sendCmd(&s.state.getMode)
// }

// func (s *civControlStruct) getDataMode() error {
// 	s.initCmd(&s.state.getDataMode, "getDataMode", []byte{254, 254, civAddress, 224, 0x1a, 0x06, 253})
// 	return s.sendCmd(&s.state.getDataMode)
// }

func (s *civControlStruct) getPwr() error {
	s.initCmd(&s.state.getPwr, "getPwr", []byte{254, 254, civAddress, 224, 0x14, 0x0a, 253})
	return s.sendCmd(&s.state.getPwr)
}

func (s *civControlStruct) getTransmitStatus() error {
	s.initCmd(&s.state.getTransmitStatus, "getTransmitStatus", []byte{254, 254, civAddress, 224, 0x1c, 0, 253})
	if err := s.sendCmd(&s.state.getTransmitStatus); err != nil {
		return err
	}
	s.initCmd(&s.state.getTuneStatus, "getTuneStatus", []byte{254, 254, civAddress, 224, 0x1c, 1, 253})
	return s.sendCmd(&s.state.getTuneStatus)
}

func (s *civControlStruct) getPreamp() error {
	s.initCmd(&s.state.getPreamp, "getPreamp", []byte{254, 254, civAddress, 224, 0x16, 0x02, 253})
	return s.sendCmd(&s.state.getPreamp)
}

func (s *civControlStruct) getAGC() error {
	s.initCmd(&s.state.getAGC, "getAGC", []byte{254, 254, civAddress, 224, 0x16, 0x12, 253})
	return s.sendCmd(&s.state.getAGC)
}

func (s *civControlStruct) getVd() error {
	s.initCmd(&s.state.getVd, "getVd", []byte{254, 254, civAddress, 224, 0x15, 0x15, 253})
	return s.sendCmd(&s.state.getVd)
}

func (s *civControlStruct) getS() error {
	s.initCmd(&s.state.getS, "getS", []byte{254, 254, civAddress, 224, 0x15, 0x02, 253})
	return s.sendCmd(&s.state.getS)
}

func (s *civControlStruct) getOVF() error {
	s.initCmd(&s.state.getOVF, "getOVF", []byte{254, 254, civAddress, 224, 0x1a, 0x09, 253})
	return s.sendCmd(&s.state.getOVF)
}

func (s *civControlStruct) getSWR() error {
	s.initCmd(&s.state.getSWR, "getSWR", []byte{254, 254, civAddress, 224, 0x15, 0x12, 253})
	return s.sendCmd(&s.state.getSWR)
}

func (s *civControlStruct) getTS() error {
	s.initCmd(&s.state.getTS, "getTS", []byte{254, 254, civAddress, 224, 0x10, 253})
	return s.sendCmd(&s.state.getTS)
}

func (s *civControlStruct) getRFGain() error {
	s.initCmd(&s.state.getRFGain, "getRFGain", []byte{254, 254, civAddress, 224, 0x14, 0x02, 253})
	return s.sendCmd(&s.state.getRFGain)
}

func (s *civControlStruct) getSQL() error {
	s.initCmd(&s.state.getSQL, "getSQL", []byte{254, 254, civAddress, 224, 0x14, 0x03, 253})
	return s.sendCmd(&s.state.getSQL)
}

func (s *civControlStruct) getNR() error {
	s.initCmd(&s.state.getNR, "getNR", []byte{254, 254, civAddress, 224, 0x14, 0x06, 253})
	return s.sendCmd(&s.state.getNR)
}

func (s *civControlStruct) getNREnabled() error {
	s.initCmd(&s.state.getNREnabled, "getNREnabled", []byte{254, 254, civAddress, 224, 0x16, 0x40, 253})
	return s.sendCmd(&s.state.getNREnabled)
}

func (s *civControlStruct) getSplit() error {
	s.initCmd(&s.state.getSplit, "getSplit", []byte{254, 254, civAddress, 224, 0x0f, 253})
	return s.sendCmd(&s.state.getSplit)
}

func (s *civControlStruct) getBothVFOFreq() error {
	s.initCmd(&s.state.getMainVFOFreq, "getMainVFOFreq", []byte{254, 254, civAddress, 224, 0x25, 0, 253})
	if err := s.sendCmd(&s.state.getMainVFOFreq); err != nil {
		return err
	}
	s.initCmd(&s.state.getSubVFOFreq, "getSubVFOFreq", []byte{254, 254, civAddress, 224, 0x25, 1, 253})
	return s.sendCmd(&s.state.getSubVFOFreq)
}

func (s *civControlStruct) getBothVFOMode() error {
	s.initCmd(&s.state.getMainVFOMode, "getMainVFOMode", []byte{254, 254, civAddress, 224, 0x26, 0, 253})
	if err := s.sendCmd(&s.state.getMainVFOMode); err != nil {
		return err
	}
	s.initCmd(&s.state.getSubVFOMode, "getSubVFOMode", []byte{254, 254, civAddress, 224, 0x26, 1, 253})
	return s.sendCmd(&s.state.getSubVFOMode)
}

func (s *civControlStruct) loop() {
	for {
		s.state.mutex.Lock()
		nextPendingCmdTimeout := time.Hour
		for i := range s.state.pendingCmds {
			diff := time.Since(s.state.pendingCmds[i].sentAt)
			if diff >= commandRetryTimeout {
				nextPendingCmdTimeout = 0
				break
			}
			if diff < nextPendingCmdTimeout {
				nextPendingCmdTimeout = diff
			}
		}
		s.state.mutex.Unlock()

		select {
		case <-s.deinitNeeded:
			s.deinitFinished <- true
			return
		case <-time.After(statusPollInterval):
			if s.state.ptt || s.state.tune {
				if !s.state.getSWR.pending && time.Since(s.state.lastSWRReceivedAt) >= statusPollInterval {
					_ = s.getSWR()
				}
			} else {
				if !s.state.getS.pending && time.Since(s.state.lastSReceivedAt) >= statusPollInterval {
					_ = s.getS()
				}
				if !s.state.getOVF.pending && time.Since(s.state.lastOVFReceivedAt) >= statusPollInterval {
					_ = s.getOVF()
				}
			}
			if !s.state.getMainVFOFreq.pending && !s.state.getSubVFOFreq.pending &&
				time.Since(s.state.lastVFOFreqReceivedAt) >= statusPollInterval {
				_ = s.getBothVFOFreq()
			}
		case <-s.resetSReadTimer:
		case <-s.newPendingCmdAdded:
		case <-time.After(nextPendingCmdTimeout):
			s.state.mutex.Lock()
			for _, cmd := range s.state.pendingCmds {
				if time.Since(cmd.sentAt) >= commandRetryTimeout {
					log.Debug("retrying cmd send ", cmd.name)
					_ = s.sendCmd(cmd)
				}
			}
			s.state.mutex.Unlock()
		}
	}
}

func (s *civControlStruct) init(st *serialStream) error {
	s.st = st

	if err := s.getBothVFOFreq(); err != nil {
		return err
	}
	if err := s.getBothVFOMode(); err != nil {
		return err
	}
	if err := s.getPwr(); err != nil {
		return err
	}
	if err := s.getTransmitStatus(); err != nil {
		return err
	}
	if err := s.getPreamp(); err != nil {
		return err
	}
	if err := s.getAGC(); err != nil {
		return err
	}
	if err := s.getVd(); err != nil {
		return err
	}
	if err := s.getS(); err != nil {
		return err
	}
	if err := s.getOVF(); err != nil {
		return err
	}
	if err := s.getSWR(); err != nil {
		return err
	}
	if err := s.getTS(); err != nil {
		return err
	}
	if err := s.getRFGain(); err != nil {
		return err
	}
	if err := s.getSQL(); err != nil {
		return err
	}
	if err := s.getNR(); err != nil {
		return err
	}
	if err := s.getNREnabled(); err != nil {
		return err
	}
	if err := s.getSplit(); err != nil {
		return err
	}

	s.deinitNeeded = make(chan bool)
	s.deinitFinished = make(chan bool)
	s.resetSReadTimer = make(chan bool)
	s.newPendingCmdAdded = make(chan bool)
	go s.loop()
	return nil
}

func (s *civControlStruct) deinit() {
	if s.deinitNeeded == nil {
		return
	}

	s.deinitNeeded <- true
	<-s.deinitFinished
	s.deinitNeeded = nil
	s.st = nil
}
