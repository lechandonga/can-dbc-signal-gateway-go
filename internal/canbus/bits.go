package canbus

import (
	"math"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// SignalBitPositions 返回信号占用的线性 CAN 位号。
func SignalBitPositions(sig *model.Signal) ([]int, bool) {
	switch sig.Order {
	case model.Intel:
		bits := make([]int, sig.Length)
		for i := 0; i < sig.Length; i++ {
			bits[i] = sig.StartBit + i
		}
		return bits, true
	case model.Motorola:
		pos := sig.StartBit
		bits := make([]int, sig.Length)
		for i := 0; i < sig.Length; i++ {
			if i > 0 {
				if pos%8 == 0 {
					pos += 15
				} else {
					pos--
				}
			}
			bits[i] = pos
		}
		return bits, true
	}
	return nil, false
}

// ExtractRaw 按定义从数据中取出无符号原始值（MSB->LSB 线性位序与 dbc.linearBits 一致）。
// 调用方需保证数据长度足够，否则访问越界由上层校验拦截。
func ExtractRaw(sig *model.Signal, data []byte) uint64 {
	var raw uint64
	switch sig.Order {
	case model.Intel:
		for i := 0; i < sig.Length; i++ {
			bit := sig.StartBit + i
			if data[bit/8]&(1<<(bit%8)) != 0 {
				raw |= 1 << uint(i)
			}
		}
	case model.Motorola:
		pos := sig.StartBit
		for i := 0; i < sig.Length; i++ {
			if i > 0 {
				if pos%8 == 0 {
					pos += 15
				} else {
					pos--
				}
			}
			linear := 8*(pos/8) + (pos % 8)
			if data[linear/8]&(1<<(linear%8)) != 0 {
				raw |= 1 << uint(sig.Length-1-i)
			}
		}
	}
	return raw
}

// ToSigned 对指定位宽的原始值做二补码符号扩展。
func ToSigned(raw uint64, length int) int64 {
	if length >= 64 {
		return int64(raw)
	}
	if raw&(1<<uint(length-1)) != 0 {
		return int64(raw) - (1 << uint(length))
	}
	return int64(raw)
}

// Physical 将原始值换算为物理量：value = raw*factor + offset。
func Physical(sig *model.Signal, raw uint64) float64 {
	if sig.IsSigned {
		return float64(ToSigned(raw, sig.Length))*sig.Factor + sig.Offset
	}
	return float64(raw)*sig.Factor + sig.Offset
}

// EncodeRaw 是 ExtractRaw 的逆操作，供本地帧序列构造使用。
// 写入前先清掉该信号自身占用的位，保证同一信号连续写入是完整覆盖而不是按位 OR。
func EncodeRaw(sig *model.Signal, raw uint64, data []byte) {
	clearSignalBits(sig, data)
	switch sig.Order {
	case model.Intel:
		for i := 0; i < sig.Length; i++ {
			bit := sig.StartBit + i
			if raw&(1<<uint(i)) != 0 {
				data[bit/8] |= 1 << uint(bit%8)
			}
		}
	case model.Motorola:
		pos := sig.StartBit
		for i := 0; i < sig.Length; i++ {
			if i > 0 {
				if pos%8 == 0 {
					pos += 15
				} else {
					pos--
				}
			}
			linear := 8*(pos/8) + (pos % 8)
			if raw&(1<<uint(sig.Length-1-i)) != 0 {
				data[linear/8] |= 1 << uint(linear%8)
			}
		}
	}
}

// clearSignalBits 清零信号自身占用的全部位。
func clearSignalBits(sig *model.Signal, data []byte) {
	positions, ok := SignalBitPositions(sig)
	if !ok {
		return
	}
	for _, bit := range positions {
		if bit/8 < len(data) {
			data[bit/8] &^= 1 << uint(bit%8)
		}
	}
}

// EncodePhysical 将物理量逆换算为原始值并写入 data。
// 有符号信号按二补码编码；超界会按位宽截断（构造工具负责保证输入合法）。
func EncodePhysical(sig *model.Signal, physical float64, data []byte) {
	scaled := (physical - sig.Offset) / sig.Factor
	var raw uint64
	if sig.IsSigned {
		v := int64(math.Round(scaled))
		raw = uint64(v)
	} else {
		raw = uint64(math.Round(scaled))
	}
	if sig.Length < 64 {
		raw &= (1 << uint(sig.Length)) - 1
	}
	EncodeRaw(sig, raw, data)
}
