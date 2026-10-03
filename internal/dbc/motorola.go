package dbc

// MotorolaBits 返回 DBC sawtooth（Motorola MSB-first）信号按"信号高位->低位"
// 顺序占用的线性位序号（线性位 0 = 字节 0 的最低位 bit0）。
//
// sawtooth 行号（DBC 起始位即 MSB 行号）与线性位序（bit0=byte0 的
// 最低位）一一对应：两种编号约定下 linear == pos。每走一位：处于行首
// （pos%8==0）时下跳一行（pos+=15），否则左移（pos--）。
// 调用前应通过 MotorolaLayoutOK 校验，否则返回的位可能覆盖错误字节。
func MotorolaBits(start, length int) []int {
	pos := start
	bits := make([]int, 0, length)
	for i := 0; i < length; i++ {
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

// MotorolaLayoutOK 判断 (start,length) 是否为自洽的 Motorola 布局。
// 判据（与 Vector DBEditor / cantools 一致）：MSB 所在列必须容得下信号
// 在首字节内的左向展开，即 startCol >= (start-length+1) 对 8 的非负模。
// 不满足时信号会"斜跨"字节边界（如 8|16、8|5），属于非法定义。
func MotorolaLayoutOK(start, length int) bool {
	col := start % 8
	need := mod8(start - length + 1)
	return col >= need
}

func mod8(v int) int {
	v %= 8
	if v < 0 {
		v += 8
	}
	return v
}
