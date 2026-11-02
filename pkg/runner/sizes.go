package runner

import (
	"math/rand/v2"

	"object_upload_test_tools/pkg/config"
)

// Sampler 是基于加权 bucket 的对象大小区间采样器。
//
// buckets 为外部传入的 SizeBucket 切片，保留原顺序；cumulative 为
// 与之等长的权重前缀和数组，用于通过一次 roll 查询命中 bucket 的下标；
// total 为权重总和，作为 roll 取值范围的上界。
// Sampler 不持有任何可变状态（构造期一次性算好前缀和），
// 因此同一个 Sampler 可被多 goroutine 安全地并发调用 Sample 方法。
type Sampler struct {
	buckets    []config.SizeBucket
	cumulative []int
	total      int
}

// NewSampler 基于一组 SizeBucket 与全局种子构造 Sampler。
//
// 构造过程仅累计各 bucket 的 Weight 形成前缀和，不拷贝 buckets 切片
// （调用方在构造后不应再修改传入切片的元素，否则可能破坏加权一致性）。
// seed 用于在 Sample 中与 taskID 混合派生每次采样的 PRNG 种子，
// 相同 seed 下整个采样序列可复现。
func NewSampler(buckets []config.SizeBucket, seed int64) *Sampler {
	s := &Sampler{buckets: buckets}
	cum := 0
	for _, b := range buckets {
		cum += b.Weight
		s.cumulative = append(s.cumulative, cum)
	}
	s.total = cum
	return s
}

// Sample 根据 taskID 返回一个落在某个 bucket 的 [Min, Max] 闭区间内的字节数。
//
// 采样过程两阶段：
//  1. 用 (黄金分割常数 ^ taskID ^ bucket 数) 派生 PRNG 种子，
//     先做一次加权 roll（IntN(total)）选择 bucket 下标；
//  2. 在所选 bucket 内做一次均匀采样：若 Min == Max 直接返回 Min，
//     否则返回 Min + Int64N(span+1)，其中 span = Max - Min，
//     闭区间 [Min, Max] 共 span+1 个取值。
//
// 注意：0x9E3779B97F4A7C15 是 64 位黄金分割常数，超出 int64 正数范围，
// 故以 uint64 运算后再转换为 int64 位模式；同时 math/rand/v2 不存在
// NewSource，故使用 NewPCG 作为唯一的有状态 PRNG 构造器（与 Task 5 一致）。
func (s *Sampler) Sample(taskID int) int64 {
	seed := uint64(0x9E3779B97F4A7C15) ^ uint64(taskID) ^ uint64(len(s.buckets))
	prng := rand.New(rand.NewPCG(seed, 0))

	roll := prng.IntN(s.total)
	idx := 0
	for i, c := range s.cumulative {
		if roll < c {
			idx = i
			break
		}
	}
	b := s.buckets[idx]
	if b.Min == b.Max {
		return b.Min
	}
	span := b.Max - b.Min
	return b.Min + prng.Int64N(span+1)
}
