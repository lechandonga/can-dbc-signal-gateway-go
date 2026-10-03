package canbus

import (
	"math"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// ExtractRaw 按定义从数据中取出无符号原始值（线性位序与 dbc.MotorolaBits 一致）。
// 调用方需保证数据长度足够，否则访问越界由上层校验拦截。
func ExtractRaw(sig *model.Signal, data []byte) uint64 {
	var raw uint64
	for i, bit := range SignalBits(sig) {
		if data[bit/8]&(1<<(bit%8)) != 0 {
			raw |= 1 << uint(sig.Length-1-i)
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

// SignalBits 返回信号占用的线性位（按信号高位->低位，MSB->LSB）。
// Intel 按 LSB 起始向高位增长，因此输出需反序得到 MSB->LSB；
// Motorola 使用 DBC sawtooth 规则（行号即线性位）。
func SignalBits(sig *model.Signal) []int {
	switch sig.Order {
	case model.Intel:
		bits := make([]int, sig.Length)
		for i := 0; i < sig.Length; i++ {
			bits[sig.Length-1-i] = sig.StartBit + i
		}
		return bits
	case model.Motorola:
		pos := sig.StartBit
		bits := make([]int, 0, sig.Length)
		for i := 0; i < sig.Length; i++ {
			if i > 0 {
				if pos%8 == 0 {
					pos += 15
				} else {
					pos--
				}
			}
			bits = append(bits, pos)
		}
		return bits
	}
	return nil
}

// EncodeRaw 是 ExtractRaw 的逆操作，供本地帧序列构造使用。
// 写入前先清除信号占用位，因此可在已有数据上安全重写而不残留旧位。
func EncodeRaw(sig *model.Signal, raw uint64, data []byte) {
	for i, bit := range SignalBits(sig) {
		data[bit/8] &^= 1 << uint(bit%8)
		if raw&(1<<uint(sig.Length-1-i)) != 0 {
			data[bit/8] |= 1 << uint(bit%8)
		}
	}
}

// ClearRaw 将信号占用的所有位清零（注入时保证"只动指定信号"）。
func ClearRaw(sig *model.Signal, data []byte) {
	for _, bit := range SignalBits(sig) {
		data[bit/8] &^= 1 << uint(bit%8)
	}
}

// RawBounds 返回信号位宽允许的原始值范围（有符号时为二补码范围）。
func RawBounds(sig *model.Signal) (int64, uint64) {
	if sig.IsSigned {
		return -(1 << uint(sig.Length-1)), (1 << uint(sig.Length-1)) - 1
	}
	if sig.Length >= 64 {
		return 0, ^uint64(0)
	}
	return 0, (1 << uint(sig.Length)) - 1
}

// EncodeRawChecked 先校验物理值：必须落在定义的 [min,max]，
// 且量化后的原始值必须能由信号位宽表示；任一不满足返回 ok=false。
// min/max 相等（如 [0,0]）时只允许该值。
func EncodeRawChecked(sig *model.Signal, physical float64) (uint64, bool) {
	if physical < sig.Min || physical > sig.Max {
		return 0, false
	}
	scaled := (physical - sig.Offset) / sig.Factor
	rv := math.Round(scaled)
	lo, hi := RawBounds(sig)
	if sig.IsSigned {
		if rv < float64(lo) || rv > float64(hi) {
			return 0, false
		}
		return uint64(int64(rv)), true
	}
	if rv < float64(lo) || rv > float64(hi) {
		return 0, false
	}
	return uint64(rv), true
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
