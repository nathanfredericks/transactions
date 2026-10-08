// Derived from CloakBrowser 0.3.25 (MIT); see third_party/cloakbrowser-0.3.25/LICENSE.
package human

import (
	"github.com/go-rod/rod/lib/proto"
	"math"
	"math/rand/v2"
	"time"
)

type Box struct{ X, Y, Width, Height float64 }

func ease(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}
func bezier(a, b, c, d proto.Point, t float64) proto.Point {
	u := 1 - t
	return proto.Point{X: u*u*u*a.X + 3*u*u*t*b.X + 3*u*t*t*c.X + t*t*t*d.X, Y: u*u*u*a.Y + 3*u*u*t*b.Y + 3*u*t*t*c.Y + t*t*t*d.Y}
}
func (p *Page) rawMove(x, y float64) error {
	buttons := 0
	if p.state.buttonDown {
		buttons = 1
	}
	err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: x, Y: y, Buttons: &buttons, Modifiers: p.modifiers()}).Call(p.state.raw)
	if err == nil {
		p.state.rawX, p.state.rawY = x, y
	}
	return err
}
func (p *Page) initCursor() error {
	if p.state.initialized {
		return nil
	}
	p.state.x = rangeValue(p.cfg.InitialCursorX)
	p.state.y = rangeValue(p.cfg.InitialCursorY)
	if err := p.rawMove(p.state.x, p.state.y); err != nil {
		return err
	}
	p.state.initialized = true
	return nil
}
func (p *Page) move(x, y float64) error {
	if err := p.initCursor(); err != nil {
		return err
	}
	sx, sy := p.state.x, p.state.y
	dx, dy := x-sx, y-sy
	dist := math.Hypot(dx, dy)
	if dist < 1 {
		return nil
	}
	c := p.cfg
	steps := int(math.Max(c.MouseMinSteps, math.Min(c.MouseMaxSteps, round(dist/c.MouseStepsDivisor))))
	px, py := -dy/dist, dx/dist
	b1, b2 := random(-0.3, 0.3)*dist, random(-0.3, 0.3)*dist
	start, end := proto.Point{X: sx, Y: sy}, proto.Point{X: x, Y: y}
	cp1 := proto.Point{X: sx + dx*0.25 + px*b1, Y: sy + dy*0.25 + py*b1}
	cp2 := proto.Point{X: sx + dx*0.75 + px*b2, Y: sy + dy*0.75 + py*b2}
	burst, counter := intRange(c.MouseBurstSize), 0
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		point := bezier(start, cp1, cp2, end, ease(t))
		w := math.Sin(math.Pi*t) * c.MouseWobbleMax
		if err := p.rawMove(round(point.X+(rand.Float64()-0.5)*2*w), round(point.Y+(rand.Float64()-0.5)*2*w)); err != nil {
			return err
		}
		counter++
		if counter >= burst && i < steps {
			if err := sleep(p.ctx(), rangeValue(c.MouseBurstPause)); err != nil {
				return err
			}
			counter = 0
		}
	}
	if rand.Float64() < c.MouseOvershootChance {
		distance := rangeValue(c.MouseOvershootPx)
		angle := math.Atan2(dy, dx)
		if err := p.rawMove(round(x+math.Cos(angle)*distance), round(y+math.Sin(angle)*distance)); err != nil {
			return err
		}
		if err := sleep(p.ctx(), random(30, 70)); err != nil {
			return err
		}
		if err := p.rawMove(round(x+(rand.Float64()-0.5)*4), round(y+(rand.Float64()-0.5)*4)); err != nil {
			return err
		}
	}
	// Reference cursor records the intended target, even after overshoot correction.
	p.state.x, p.state.y = x, y
	return nil
}
func (p *Page) idle(seconds float64) error {
	x, y := p.state.x, p.state.y
	until := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for time.Now().Before(until) {
		x += (rand.Float64() - 0.5) * 2 * p.cfg.IdleDriftPx
		y += (rand.Float64() - 0.5) * 2 * p.cfg.IdleDriftPx
		if err := p.rawMove(round(x), round(y)); err != nil {
			return err
		}
		if err := sleep(p.ctx(), rangeValue(p.cfg.IdlePauseRange)); err != nil {
			return err
		}
	}
	return nil
}
func (p *Page) beforeMove() error {
	if err := p.initCursor(); err != nil {
		return err
	}
	if p.cfg.IdleBetweenActions {
		return p.idle(rangeValue(p.cfg.IdleBetweenDuration))
	}
	return nil
}
func (p *Page) target(b Box, isInput bool) proto.Point {
	x, y := random(0.35, 0.65), random(0.35, 0.65)
	if isInput {
		x = rangeValue(p.cfg.ClickInputXRange)
		y = random(0.3, 0.7)
	}
	return proto.Point{X: round(b.X + b.Width*x), Y: round(b.Y + b.Height*y)}
}
func (p *Page) mouseDown(count int) error {
	p.state.buttonDown = true
	buttons := 1
	return (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, Button: proto.InputMouseButtonLeft, Buttons: &buttons, X: p.state.rawX, Y: p.state.rawY, ClickCount: count, Modifiers: p.modifiers()}).Call(p.state.raw)
}
func (p *Page) mouseUp(count int) error {
	err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, Button: proto.InputMouseButtonLeft, X: p.state.rawX, Y: p.state.rawY, ClickCount: count, Modifiers: p.modifiers()}).Call(p.state.raw)
	if err == nil {
		p.state.buttonDown = false
	}
	return err
}
func (p *Page) clickMouse(isInput bool) error {
	aim, hold := p.cfg.ClickAimDelayButton, p.cfg.ClickHoldButton
	if isInput {
		aim, hold = p.cfg.ClickAimDelayInput, p.cfg.ClickHoldInput
	}
	if err := sleep(p.ctx(), rangeValue(aim)); err != nil {
		return err
	}
	if err := p.mouseDown(1); err != nil {
		return err
	}
	if err := sleep(p.ctx(), rangeValue(hold)); err != nil {
		return err
	}
	return p.mouseUp(1)
}
