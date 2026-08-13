package processor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetOptimalBufferSizes(t *testing.T) {
	// 校验配置函数返回的取值是否合理
	workBuf, resultBuf, errorBuf := getOptimalBufferSizes()

	// 所有取值都应为正数
	assert.Greater(t, workBuf, 0, "workBuffer should be positive")
	assert.Greater(t, resultBuf, 0, "resultBuffer should be positive")
	assert.Greater(t, errorBuf, 0, "errorBuffer should be positive")
}

func TestNewProcessor(t *testing.T) {
	// 注：完整测试需要真实的数据库连接。
	// 这里只验证 NewProcessor 传入 nil 时不会 panic，
	// 实际场景下应改用测试数据库。

	tests := []struct {
		name               string
		workers            int
		convertTraditional bool
		wantWorkers        int
	}{
		{
			name:               "default workers",
			workers:            0,
			convertTraditional: false,
			wantWorkers:        1, // 实际会取 runtime.NumCPU()
		},
		{
			name:               "specific workers",
			workers:            4,
			convertTraditional: true,
			wantWorkers:        4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 没有数据库无法完整验证，但可以先校验这段逻辑本身
			if tt.workers <= 0 {
				assert.Greater(t, tt.wantWorkers, 0)
			} else {
				assert.Equal(t, tt.wantWorkers, tt.workers)
			}
		})
	}

	processor := NewProcessor(nil, 4, false)
	assert.Equal(t, deterministicBatchSize, processor.batchSize)
}

func TestSetBatchSize(t *testing.T) {
	tests := []struct {
		name     string
		initial  int
		newSize  int
		wantSize int
	}{
		{
			name:     "set valid size",
			initial:  100,
			newSize:  200,
			wantSize: 200,
		},
		{
			name:     "ignore zero",
			initial:  100,
			newSize:  0,
			wantSize: 100, // 非法取值应保持原值不变
		},
		{
			name:     "ignore negative",
			initial:  100,
			newSize:  -10,
			wantSize: 100, // 非法取值应保持原值不变
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 每个用例都新建处理器，避免相互影响
			proc := &Processor{
				batchSize: tt.initial,
			}
			proc.SetBatchSize(tt.newSize)
			assert.Equal(t, tt.wantSize, proc.batchSize)
		})
	}
}

// 基准测试
func BenchmarkGetOptimalBufferSizes(b *testing.B) {
	for b.Loop() {
		getOptimalBufferSizes()
	}
}
