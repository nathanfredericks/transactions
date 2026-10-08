// Derived from CloakBrowser 0.3.25 (MIT); see third_party/cloakbrowser-0.3.25/LICENSE.
package human

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

//go:embed presets.json
var presets []byte

type Config struct {
	TypingDelay           float64    `json:"typing_delay"`
	TypingDelaySpread     float64    `json:"typing_delay_spread"`
	TypingPauseChance     float64    `json:"typing_pause_chance"`
	TypingPauseRange      [2]float64 `json:"typing_pause_range"`
	ShiftDownDelay        [2]float64 `json:"shift_down_delay"`
	ShiftUpDelay          [2]float64 `json:"shift_up_delay"`
	KeyHold               [2]float64 `json:"key_hold"`
	FieldSwitchDelay      [2]float64 `json:"field_switch_delay"`
	MistypeChance         float64    `json:"mistype_chance"`
	MistypeDelayNotice    [2]float64 `json:"mistype_delay_notice"`
	MistypeDelayCorrect   [2]float64 `json:"mistype_delay_correct"`
	MouseStepsDivisor     float64    `json:"mouse_steps_divisor"`
	MouseMinSteps         float64    `json:"mouse_min_steps"`
	MouseMaxSteps         float64    `json:"mouse_max_steps"`
	MouseWobbleMax        float64    `json:"mouse_wobble_max"`
	MouseOvershootChance  float64    `json:"mouse_overshoot_chance"`
	MouseOvershootPx      [2]float64 `json:"mouse_overshoot_px"`
	MouseBurstSize        [2]float64 `json:"mouse_burst_size"`
	MouseBurstPause       [2]float64 `json:"mouse_burst_pause"`
	ClickAimDelayInput    [2]float64 `json:"click_aim_delay_input"`
	ClickAimDelayButton   [2]float64 `json:"click_aim_delay_button"`
	ClickHoldInput        [2]float64 `json:"click_hold_input"`
	ClickHoldButton       [2]float64 `json:"click_hold_button"`
	ClickInputXRange      [2]float64 `json:"click_input_x_range"`
	IdleDriftPx           float64    `json:"idle_drift_px"`
	IdlePauseRange        [2]float64 `json:"idle_pause_range"`
	ScrollDeltaBase       [2]float64 `json:"scroll_delta_base"`
	ScrollDeltaVariance   float64    `json:"scroll_delta_variance"`
	ScrollPauseFast       [2]float64 `json:"scroll_pause_fast"`
	ScrollPauseSlow       [2]float64 `json:"scroll_pause_slow"`
	ScrollAccelSteps      [2]float64 `json:"scroll_accel_steps"`
	ScrollDecelSteps      [2]float64 `json:"scroll_decel_steps"`
	ScrollOvershootChance float64    `json:"scroll_overshoot_chance"`
	ScrollOvershootPx     [2]float64 `json:"scroll_overshoot_px"`
	ScrollSettleDelay     [2]float64 `json:"scroll_settle_delay"`
	ScrollTargetZone      [2]float64 `json:"scroll_target_zone"`
	ScrollPreMoveDelay    [2]float64 `json:"scroll_pre_move_delay"`
	InitialCursorX        [2]float64 `json:"initial_cursor_x"`
	InitialCursorY        [2]float64 `json:"initial_cursor_y"`
	IdleBetweenActions    bool       `json:"idle_between_actions"`
	IdleBetweenDuration   [2]float64 `json:"idle_between_duration"`
}

func Resolve(preset string, overrides map[string]json.RawMessage) (Config, error) {
	if preset == "" {
		preset = "default"
	}
	var all map[string]map[string]json.RawMessage
	if err := json.Unmarshal(presets, &all); err != nil {
		return Config{}, err
	}
	base, ok := all[preset]
	if !ok {
		return Config{}, errors.New("unknown human preset")
	}
	for k, v := range overrides {
		if _, ok := base[k]; !ok {
			return Config{}, errors.New("unknown human parameter")
		}
		base[k] = v
	}
	data, _ := json.Marshal(base)
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	// Refuse invalid parameters before they can produce unbounded work.
	for name, value := range base {
		if name == "idle_between_actions" {
			continue
		}
		var number float64
		if json.Unmarshal(value, &number) == nil {
			if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
				return c, errors.New("invalid human parameter")
			}
			continue
		}
		var r [2]float64
		if json.Unmarshal(value, &r) != nil || r[0] < 0 || r[1] < r[0] || math.IsInf(r[1], 0) {
			return c, errors.New("invalid human range")
		}
	}
	if c.MouseStepsDivisor <= 0 || c.MouseMinSteps < 1 || c.MouseMaxSteps < c.MouseMinSteps || c.MouseBurstSize[0] < 1 || c.ScrollDeltaBase[0] <= 0 || c.ScrollDeltaVariance > 1 {
		return c, errors.New("invalid mouse steps")
	}
	for _, p := range []float64{c.TypingPauseChance, c.MistypeChance, c.MouseOvershootChance, c.ScrollOvershootChance} {
		if p > 1 {
			return c, errors.New("invalid probability")
		}
	}
	if c.ScrollTargetZone[1] > 1 || c.ClickInputXRange[1] > 1 {
		return c, errors.New("invalid target zone")
	}
	return c, nil
}
func random(min, max float64) float64 { return min + rand.Float64()*(max-min) }
func rangeValue(r [2]float64) float64 { return random(r[0], r[1]) }
func intRange(r [2]float64) int       { return int(math.Floor(random(r[0], r[1]+1))) }
func round(v float64) float64         { return math.Floor(v + 0.5) }
func sleep(ctx context.Context, ms float64) error {
	timer := time.NewTimer(time.Duration(ms * float64(time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
