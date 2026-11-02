package runner

import (
	"object_upload_test_tools/pkg/config"
	"testing"
)

// TestSamplerDistribution 验证加权采样在大量样本下分布与权重一致。
// 三个 bucket 权重 50/30/20，10000 次采样后各 bucket 落点计数
// 应落在期望值 ±250 容差范围内（PCG 高质量 PRNG 下统计上稳定）。
func TestSamplerDistribution(t *testing.T) {
	buckets := []config.SizeBucket{
		{Min: 1, Max: 1024, Weight: 50},
		{Min: 1024, Max: 1048576, Weight: 30},
		{Min: 1048576, Max: 10485760, Weight: 20},
	}
	s := NewSampler(buckets, 42)
	counts := map[int]int{0: 0, 1: 0, 2: 0}
	for i := 0; i < 10000; i++ {
		sz := s.Sample(i)
		switch {
		case sz < 1024:
			counts[0]++
		case sz < 1048576:
			counts[1]++
		default:
			counts[2]++
		}
	}
	if counts[0] < 4750 || counts[0] > 5250 {
		t.Errorf("bucket 0 = %d, want ~5000", counts[0])
	}
	if counts[1] < 2750 || counts[1] > 3250 {
		t.Errorf("bucket 1 = %d, want ~3000", counts[1])
	}
	if counts[2] < 1750 || counts[2] > 2250 {
		t.Errorf("bucket 2 = %d, want ~2000", counts[2])
	}
}

// TestSamplerDeterministic 验证相同 (seed, taskID, buckets) 输入下
// Sample 输出完全一致。两个独立构造的 Sampler 对 100 个 taskID
// 必须逐项相等。
func TestSamplerDeterministic(t *testing.T) {
	buckets := []config.SizeBucket{{Min: 1, Max: 100, Weight: 100}}
	s1 := NewSampler(buckets, 42)
	s2 := NewSampler(buckets, 42)
	for i := 0; i < 100; i++ {
		if s1.Sample(i) != s2.Sample(i) {
			t.Errorf("non-deterministic at i=%d", i)
			break
		}
	}
}

// TestSamplerFixedBucket 验证 Min == Max 的固定 bucket 场景下，
// Sample 始终返回该固定值（不进入 PRNG 取值路径）。
func TestSamplerFixedBucket(t *testing.T) {
	buckets := []config.SizeBucket{{Min: 512, Max: 512, Weight: 100}}
	s := NewSampler(buckets, 1)
	for i := 0; i < 100; i++ {
		if got := s.Sample(i); got != 512 {
			t.Errorf("got %d, want 512", got)
		}
	}
}

// TestSamplerBounds 验证 Sample 输出始终落在所选 bucket 的 [Min, Max]
// 闭区间内。1000 次采样逐一断言上界与下界。
func TestSamplerBounds(t *testing.T) {
	buckets := []config.SizeBucket{
		{Min: 100, Max: 1000, Weight: 100},
	}
	s := NewSampler(buckets, 7)
	for i := 0; i < 1000; i++ {
		sz := s.Sample(i)
		if sz < 100 || sz > 1000 {
			t.Errorf("size %d out of bounds [100,1000]", sz)
		}
	}
}

// TestSamplerSeedAffectsSequence 验证 NewSampler 的 seed 参数真正影响采样序列：
// 两个 Sampler 用相同的 buckets 与 taskID 范围，但 seed 不同，
// 其输出序列必须存在差异（否则 seed 为死参数）。
// 此测试针对 Task 15 review 的 Important I1——seed 死参数缺陷已修复，
// 此测试固化修复行为，防止回归。
func TestSamplerSeedAffectsSequence(t *testing.T) {
	buckets := []config.SizeBucket{{Min: 1, Max: 1000000, Weight: 100}}
	s1 := NewSampler(buckets, 42)
	s2 := NewSampler(buckets, 999)
	diffCount := 0
	for i := 0; i < 100; i++ {
		if s1.Sample(i) != s2.Sample(i) {
			diffCount++
		}
	}
	if diffCount == 0 {
		t.Errorf("seed 未影响采样序列：100 次采样全部相同，seed 为死参数")
	}
}
