// Derived from CloakBrowser 0.3.25 (MIT); see third_party/cloakbrowser-0.3.25/LICENSE.
package human

import (
	"github.com/go-rod/rod/lib/proto"
	"math"
	"math/rand/v2"
)

func (p *Page) inViewport(b Box, height float64) bool {
	return b.Y >= height*p.cfg.ScrollTargetZone[0] && b.Y+b.Height <= height*p.cfg.ScrollTargetZone[1]
}
func (p *Page) smoothWheel(delta float64) error {
	sent := 0.0
	sign := 1.0
	if delta < 0 {
		sign = -1
	}
	for sent < math.Abs(delta) {
		chunk := min(random(20, 40), math.Abs(delta)-sent)
		if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel, X: p.state.rawX, Y: p.state.rawY, DeltaY: round(chunk) * sign, Modifiers: p.modifiers()}).Call(p.state.raw); err != nil {
			return err
		}
		sent += chunk
		if err := sleep(p.ctx(), random(8, 20)); err != nil {
			return err
		}
	}
	return nil
}
func (p *Page) scrollTo(el *Element) (Box, error) {
	viewport, err := p.Evaluate("({width:innerWidth,height:innerHeight})")
	if err != nil {
		return Box{}, err
	}
	width, height := viewport.Value.Get("width").Num(), viewport.Value.Get("height").Num()
	box, err := el.box()
	if err != nil {
		if err = sleep(p.ctx(), 200); err != nil {
			return Box{}, err
		}
		box, err = el.box()
		if err != nil {
			return Box{}, err
		}
	}
	if p.inViewport(box, height) {
		return box, nil
	}
	if err = p.move(round(width*random(0.3, 0.7)), round(height*random(0.3, 0.7))); err != nil {
		return Box{}, err
	}
	if err = sleep(p.ctx(), rangeValue(p.cfg.ScrollPreMoveDelay)); err != nil {
		return Box{}, err
	}
	targetY := height * rangeValue(p.cfg.ScrollTargetZone)
	distance := box.Y + box.Height/2 - targetY
	direction := 1.0
	if distance < 0 {
		direction = -1
	}
	absDistance := math.Abs(distance)
	average := (p.cfg.ScrollDeltaBase[0] + p.cfg.ScrollDeltaBase[1]) / 2
	steps := int(max(3, math.Ceil(absDistance/average)))
	accel, decel := intRange(p.cfg.ScrollAccelSteps), intRange(p.cfg.ScrollDecelSteps)
	scrolled := 0.0
	for i := 0; i < steps; i++ {
		var delta, pause float64
		if i < accel {
			delta = random(80, 100)
			pause = rangeValue(p.cfg.ScrollPauseSlow)
		} else if i >= steps-decel {
			delta = random(60, 90)
			pause = rangeValue(p.cfg.ScrollPauseSlow)
		} else {
			delta = rangeValue(p.cfg.ScrollDeltaBase)
			pause = rangeValue(p.cfg.ScrollPauseFast)
		}
		delta = round(delta*(1+(rand.Float64()-0.5)*2*p.cfg.ScrollDeltaVariance)) * direction
		if err = p.smoothWheel(delta); err != nil {
			return Box{}, err
		}
		scrolled += math.Abs(delta)
		if err = sleep(p.ctx(), pause); err != nil {
			return Box{}, err
		}
		if i%3 == 2 || i == steps-1 {
			box, err = el.box()
			if err == nil && p.inViewport(box, height) {
				break
			}
		}
		if scrolled >= absDistance*1.1 {
			break
		}
	}
	if rand.Float64() < p.cfg.ScrollOvershootChance {
		if err = p.smoothWheel(round(rangeValue(p.cfg.ScrollOvershootPx)) * direction); err != nil {
			return Box{}, err
		}
		if err = sleep(p.ctx(), rangeValue(p.cfg.ScrollSettleDelay)); err != nil {
			return Box{}, err
		}
		count := intRange([2]float64{1, 2})
		for i := 0; i < count; i++ {
			if err = p.smoothWheel(round(random(40, 80)) * -direction); err != nil {
				return Box{}, err
			}
			if err = sleep(p.ctx(), random(100, 250)); err != nil {
				return Box{}, err
			}
		}
	}
	if err = sleep(p.ctx(), rangeValue(p.cfg.ScrollSettleDelay)); err != nil {
		return Box{}, err
	}
	return el.box()
}
