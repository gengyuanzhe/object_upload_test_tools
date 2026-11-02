package content

import (
	"encoding/binary"
	"io"
	"math/rand/v2"
)

// RandomGenerator 基于 math/rand/v2 的 PCG 伪随机源生成确定性字节流。
// BaseSeed 是任务级基础种子；与 taskID 相加后派生出每次 Generate 的实际种子，
// 同一 (BaseSeed, taskID) 组合产生完全一致的字节序列，
// 不同 taskID 则保证并发上传的内容互不相同。
type RandomGenerator struct {
	BaseSeed int64
}

// Type 返回内容类型标识 "random"。
func (g *RandomGenerator) Type() string { return "random" }

// Generate 返回一个流式 Reader，读取时按需生成至多 size 字节的伪随机数据。
// 返回的 Reader 不可并发使用；多次 Generate 调用之间互不影响。
func (g *RandomGenerator) Generate(size int64, taskID int) (io.Reader, error) {
	seed := g.BaseSeed + int64(taskID)
	// math/rand/v2 不再提供 NewSource，统一使用 PCG 源：seed1 取派生种子，
	// seed2 固定为 0，以保证 (BaseSeed, taskID) -> 字节流 的映射是纯函数。
	prng := rand.New(rand.NewPCG(uint64(seed), 0))
	return &randReader{prng: prng, remaining: size}, nil
}

// randReader 把对 PRNG 的字节读取封装为 io.Reader。
// 每次调用 Read 时按需填充切片，并扣减剩余字节数，
// 直到 remaining 归零后返回 io.EOF，从而保证总输出长度恰好等于 size。
type randReader struct {
	prng      *rand.Rand
	remaining int64
}

// Read 实现 io.Reader。当 remaining <= 0 时返回 EOF；
// 否则将 p 截断为不超过 remaining 的前缀，用 PRNG 填满该前缀，
// 再扣减剩余字节数。注意 math/rand/v2 的 Rand 没有 Read 方法，
// 因此使用 Uint64 + encoding/binary 的方式按 8 字节一组填充，
// 末尾不足 8 字节时按实际长度写入小端字节序。
func (r *randReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	// 以 8 字节为单位填充，剩余尾段按实际长度写入。
	var buf [8]byte
	filled := int64(0)
	for filled+8 <= n {
		binary.LittleEndian.PutUint64(buf[:], r.prng.Uint64())
		copy(p[filled:filled+8], buf[:])
		filled += 8
	}
	if filled < n {
		binary.LittleEndian.PutUint64(buf[:], r.prng.Uint64())
		copy(p[filled:n], buf[:n-filled])
		filled = n
	}
	r.remaining -= n
	if r.remaining <= 0 {
		return int(n), io.EOF
	}
	return int(n), nil
}
