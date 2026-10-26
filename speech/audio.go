package speech

import "encoding/binary"

func WAV(pcm []byte, rate uint32) ([]byte, error) {
	if len(pcm) == 0 || len(pcm) > MaxOutputBytes || len(pcm)%2 != 0 || (rate != 16000 && rate != 24000) {
		return nil, ErrInvalid
	}
	out := make([]byte, 44+len(pcm))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], rate)
	binary.LittleEndian.PutUint32(out[28:], rate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out, nil
}
func ParseWAV(raw []byte) ([]byte, error) {
	if len(raw) < 44 || len(raw) > MaxInputSamples*2+4096 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(raw[4:]))+8 != uint64(len(raw)) {
		return nil, ErrInvalid
	}
	format := false
	var pcm []byte
	for offset := 12; offset < len(raw); {
		if offset+8 > len(raw) {
			return nil, ErrInvalid
		}
		size := uint64(binary.LittleEndian.Uint32(raw[offset+4:]))
		end := uint64(offset+8) + size
		if end > uint64(len(raw)) {
			return nil, ErrInvalid
		}
		chunk := raw[offset+8 : end]
		switch string(raw[offset : offset+4]) {
		case "fmt ":
			if format || len(chunk) < 16 || binary.LittleEndian.Uint16(chunk) != 1 || binary.LittleEndian.Uint16(chunk[2:]) != 1 || binary.LittleEndian.Uint32(chunk[4:]) != 16000 || binary.LittleEndian.Uint32(chunk[8:]) != 32000 || binary.LittleEndian.Uint16(chunk[12:]) != 2 || binary.LittleEndian.Uint16(chunk[14:]) != 16 {
				return nil, ErrInvalid
			}
			format = true
		case "data":
			if pcm != nil || len(chunk) == 0 || len(chunk) > MaxInputSamples*2 || len(chunk)%2 != 0 {
				return nil, ErrInvalid
			}
			pcm = chunk
		}
		offset = int(end + size%2)
		if offset > len(raw) {
			return nil, ErrInvalid
		}
	}
	if !format || pcm == nil {
		return nil, ErrInvalid
	}
	return pcm, nil
}
