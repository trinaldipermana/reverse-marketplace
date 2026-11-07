package commerce

import (
	"errors"
	"math"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func TestEffectiveMinimum(t *testing.T) {
	cases := []struct {
		name        string
		list, floor int64
		discount    int
		want        int64
	}{
		{"floor stricter", 5_800_000, 5_400_000, 1000, 5_400_000},
		{"discount stricter", 6_000_000, 5_000_000, 1000, 5_400_000},
		{"round up", 101, 0, 1000, 91},
		{"one rupiah", 1, 0, 9999, 1},
		{"no discount", 101, 0, 0, 101},
		{"all discount still floor", 100, 1, 10000, 1},
		{"max int no overflow", math.MaxInt64, 0, 0, math.MaxInt64},
		{"max int discounted", math.MaxInt64, 0, 5000, math.MaxInt64/2 + 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := effectiveMinimum(tt.list, domain.Rules{FloorPriceIDR: tt.floor, MaxDiscountBPS: tt.discount})
			if err != nil || got != tt.want {
				t.Fatalf("minimum=%d err=%v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestEffectiveMinimumRejectsInvalidPolicy(t *testing.T) {
	for _, input := range []struct {
		list, floor int64
		discount    int
	}{
		{0, 0, 0}, {-1, 0, 0}, {100, -1, 0}, {100, 0, -1}, {100, 0, 10001},
	} {
		_, err := effectiveMinimum(input.list, domain.Rules{FloorPriceIDR: input.floor, MaxDiscountBPS: input.discount})
		var failure *contracts.Error
		if !errors.As(err, &failure) || failure.Code != contracts.InvalidState {
			t.Fatalf("invalid policy accepted: %+v %v", input, err)
		}
	}
}
