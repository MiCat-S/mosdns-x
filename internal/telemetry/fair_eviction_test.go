package telemetry

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestFairShareEvictionSparesLightUsers(t *testing.T) {
	counts := map[string]uint64{"heavy": 900, "medium": 80, "light": 20}
	plan := fairShareEviction(counts, 100)
	if plan["light"] != 0 || plan["medium"] != 0 {
		t.Fatalf("light or medium user lost records: %v", plan)
	}
	if plan["heavy"] != 100 {
		t.Fatalf("heavy should give all 100: %v", plan)
	}
}

func TestFairShareEvictionLevelsTopUsers(t *testing.T) {
	counts := map[string]uint64{"a": 500, "b": 480, "c": 100}
	plan := fairShareEviction(counts, 100)
	after := map[string]uint64{}
	for u, n := range counts {
		after[u] = n - plan[u]
	}
	// 100 must come from a and b, leveled: 980-100 = 880 over two users.
	if after["c"] != 100 {
		t.Fatalf("c was touched: %v", after)
	}
	if after["a"]+after["b"] != 880 || diff(after["a"], after["b"]) > 1 {
		t.Fatalf("a and b not leveled: %v", after)
	}
}

// Properties over random inputs: removal is exact, no user loses more than it
// holds, users outside the cut keep everything, and cut users end within one
// record of each other and at or above every untouched user.
func TestFairShareEvictionProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 2000; iter++ {
		counts := map[string]uint64{}
		var total uint64
		for i := 0; i < 1+r.IntN(8); i++ {
			n := uint64(r.IntN(50))
			counts[fmt.Sprintf("u%d", i)] = n
			total += n
		}
		if total == 0 {
			continue
		}
		over := uint64(1 + r.IntN(int(total)))
		plan := fairShareEviction(counts, over)
		var removed uint64
		var minCut, maxKept uint64 = ^uint64(0), 0
		for u, n := range counts {
			take := plan[u]
			if take > n {
				t.Fatalf("iter %d: %s loses %d of %d", iter, u, take, n)
			}
			removed += take
			if take > 0 {
				minCut = min(minCut, n-take)
			} else {
				maxKept = max(maxKept, n)
			}
		}
		if removed != over {
			t.Fatalf("iter %d: removed %d, want %d (counts=%v plan=%v)", iter, removed, over, counts, plan)
		}
		if minCut != ^uint64(0) && maxKept > minCut+1 {
			t.Fatalf("iter %d: an untouched user (%d) holds more than a cut one (%d): counts=%v plan=%v", iter, maxKept, minCut, counts, plan)
		}
	}
}

func TestFairShareEvictionRemovesEverythingWhenAsked(t *testing.T) {
	counts := map[string]uint64{"a": 3, "b": 2}
	plan := fairShareEviction(counts, 10)
	if plan["a"] != 3 || plan["b"] != 2 {
		t.Fatalf("plan=%v", plan)
	}
}

func diff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
