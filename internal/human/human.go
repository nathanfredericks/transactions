// Package human drives a page's mouse and keyboard the way a person does:
// curved, bursty mouse paths, aimed clicks, scrolling in steps, and typing with
// pauses and the odd corrected typo. It is a port of CloakBrowser 0.3.25's
// careful preset (MIT; see third_party/cloakbrowser-0.3.25/LICENSE) and sends
// the same CDP input events the earlier Rod port did, through a Playwright CDP
// session.
package human

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// ErrElement means the target has no box to aim at.
var ErrElement = errors.New("human: element has no box")

// Cursor is one page's humanized input. Its methods are safe to call from one
// goroutine at a time; a mutex guards against overlap.
type Cursor struct {
	page playwright.Page
	cdp  playwright.CDPSession
	cfg  config

	mu               sync.Mutex
	x, y, rawX, rawY float64
	initialized      bool
	buttonDown       bool
	keys             map[keyInfo]bool
	symbol           map[string]any
}

// New attaches a cursor to a page of the browser context.
func New(bctx playwright.BrowserContext, p playwright.Page) (*Cursor, error) {
	cdp, err := bctx.NewCDPSession(p)
	if err != nil {
		return nil, err
	}
	return &Cursor{page: p, cdp: cdp, cfg: careful, keys: map[keyInfo]bool{}}, nil
}

// Click moves to the element, scrolling it into the comfortable zone first,
// and clicks it.
func (c *Cursor) Click(ctx context.Context, l playwright.Locator) error {
	return c.action(ctx, func() error { return c.click(ctx, l, true) })
}

// ClickInPlace moves to the element where it is and clicks it, without scrolling.
func (c *Cursor) ClickInPlace(ctx context.Context, l playwright.Locator) error {
	return c.action(ctx, func() error { return c.click(ctx, l, false) })
}

// Fill pauses as when switching fields, clicks the field, clears it and types
// the value.
func (c *Cursor) Fill(ctx context.Context, l playwright.Locator, value string) error {
	return c.action(ctx, func() error {
		if err := sleep(ctx, rangeValue(c.cfg.FieldSwitchDelay)); err != nil {
			return err
		}
		if err := c.click(ctx, l, true); err != nil {
			return err
		}
		if err := sleep(ctx, random(100, 250)); err != nil {
			return err
		}
		if err := c.selectAll(ctx); err != nil {
			return err
		}
		if err := sleep(ctx, random(30, 80)); err != nil {
			return err
		}
		if err := c.key(ctx, backspace); err != nil {
			return err
		}
		if err := sleep(ctx, random(50, 150)); err != nil {
			return err
		}
		return c.typeText(ctx, value)
	})
}

// PressSequentially clicks the field unless it has focus, then types the value.
func (c *Cursor) PressSequentially(ctx context.Context, l playwright.Locator, value string) error {
	return c.action(ctx, func() error {
		focused, err := l.And(c.page.Locator(":focus")).Count()
		if err != nil {
			return err
		}
		if focused == 0 {
			if err = c.click(ctx, l, true); err != nil {
				return err
			}
		}
		if err = sleep(ctx, random(100, 250)); err != nil {
			return err
		}
		return c.typeText(ctx, value)
	})
}

// action serializes input and releases any key left held by a failed step.
func (c *Cursor) action(ctx context.Context, fn func() error) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		if err != nil {
			c.release()
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (c *Cursor) release() {
	if c.symbol != nil {
		up := c.symbol
		up["type"], up["text"], up["unmodifiedText"] = "keyUp", "", ""
		_, _ = c.cdp.Send("Input.dispatchKeyEvent", up)
		c.symbol = nil
	}
	for k := range c.keys {
		_, _ = c.cdp.Send("Input.dispatchKeyEvent", k.event("keyUp", 0))
		delete(c.keys, k)
	}
}

func (c *Cursor) click(ctx context.Context, l playwright.Locator, scroll bool) error {
	isInput, err := c.moveTo(ctx, l, scroll)
	if err != nil {
		return err
	}
	aim, hold := c.cfg.ClickAimDelayButton, c.cfg.ClickHoldButton
	if isInput {
		aim, hold = c.cfg.ClickAimDelayInput, c.cfg.ClickHoldInput
	}
	if err = sleep(ctx, rangeValue(aim)); err != nil {
		return err
	}
	if err = c.mouseDown(); err != nil {
		return err
	}
	if err = sleep(ctx, rangeValue(hold)); err != nil {
		return err
	}
	return c.mouseUp()
}

// moveTo idles, scrolls the element into view if asked, and moves to a point
// inside it: near the left of an input, near the middle of anything else.
func (c *Cursor) moveTo(ctx context.Context, l playwright.Locator, scroll bool) (bool, error) {
	if err := c.beforeMove(ctx); err != nil {
		return false, err
	}
	if scroll {
		if _, err := c.scrollTo(ctx, l); err != nil {
			return false, err
		}
	}
	// Wait until the element is visible, stable, enabled and not covered. A
	// trial click checks that and sends no input.
	if err := l.Click(playwright.LocatorClickOptions{Trial: new(true), Timeout: timeout(ctx)}); err != nil {
		return false, err
	}
	b, err := elementBox(l)
	if err != nil {
		return false, err
	}
	inputs, err := l.And(c.page.Locator(`input,textarea,[contenteditable="true"]`)).Count()
	if err != nil {
		return false, err
	}
	isInput := inputs > 0
	x, y := random(0.35, 0.65), random(0.35, 0.65)
	if isInput {
		x, y = rangeValue(c.cfg.ClickInputXRange), random(0.3, 0.7)
	}
	return isInput, c.move(ctx, round(b.X+b.Width*x), round(b.Y+b.Height*y))
}

type box struct{ X, Y, Width, Height float64 }

func elementBox(l playwright.Locator) (box, error) {
	r, err := l.BoundingBox()
	if err != nil {
		return box{}, err
	}
	if r == nil || r.Width <= 0 || r.Height <= 0 {
		return box{}, ErrElement
	}
	return box{r.X, r.Y, r.Width, r.Height}, nil
}

func (c *Cursor) beforeMove(ctx context.Context) error {
	if err := c.initCursor(); err != nil {
		return err
	}
	if c.cfg.IdleBetweenActions {
		return c.idle(ctx, rangeValue(c.cfg.IdleBetweenDuration))
	}
	return nil
}

func (c *Cursor) initCursor() error {
	if c.initialized {
		return nil
	}
	c.x, c.y = rangeValue(c.cfg.InitialCursorX), rangeValue(c.cfg.InitialCursorY)
	if err := c.rawMove(c.x, c.y); err != nil {
		return err
	}
	c.initialized = true
	return nil
}

func (c *Cursor) idle(ctx context.Context, seconds float64) error {
	x, y := c.x, c.y
	until := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for time.Now().Before(until) {
		x += (rand.Float64() - 0.5) * 2 * c.cfg.IdleDriftPx
		y += (rand.Float64() - 0.5) * 2 * c.cfg.IdleDriftPx
		if err := c.rawMove(round(x), round(y)); err != nil {
			return err
		}
		if err := sleep(ctx, rangeValue(c.cfg.IdlePauseRange)); err != nil {
			return err
		}
	}
	return nil
}

// move follows an eased Bézier path in bursts, with wobble and the occasional
// overshoot and correction.
func (c *Cursor) move(ctx context.Context, x, y float64) error {
	if err := c.initCursor(); err != nil {
		return err
	}
	sx, sy := c.x, c.y
	dx, dy := x-sx, y-sy
	dist := math.Hypot(dx, dy)
	if dist < 1 {
		return nil
	}
	cfg := c.cfg
	steps := int(math.Max(cfg.MouseMinSteps, math.Min(cfg.MouseMaxSteps, round(dist/cfg.MouseStepsDivisor))))
	px, py := -dy/dist, dx/dist
	b1, b2 := random(-0.3, 0.3)*dist, random(-0.3, 0.3)*dist
	start, end := point{sx, sy}, point{x, y}
	cp1 := point{sx + dx*0.25 + px*b1, sy + dy*0.25 + py*b1}
	cp2 := point{sx + dx*0.75 + px*b2, sy + dy*0.75 + py*b2}
	burst, counter := intRange(cfg.MouseBurstSize), 0
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		p := bezier(start, cp1, cp2, end, ease(t))
		w := math.Sin(math.Pi*t) * cfg.MouseWobbleMax
		if err := c.rawMove(round(p.X+(rand.Float64()-0.5)*2*w), round(p.Y+(rand.Float64()-0.5)*2*w)); err != nil {
			return err
		}
		counter++
		if counter >= burst && i < steps {
			if err := sleep(ctx, rangeValue(cfg.MouseBurstPause)); err != nil {
				return err
			}
			counter = 0
		}
	}
	if rand.Float64() < cfg.MouseOvershootChance {
		distance := rangeValue(cfg.MouseOvershootPx)
		angle := math.Atan2(dy, dx)
		if err := c.rawMove(round(x+math.Cos(angle)*distance), round(y+math.Sin(angle)*distance)); err != nil {
			return err
		}
		if err := sleep(ctx, random(30, 70)); err != nil {
			return err
		}
		if err := c.rawMove(round(x+(rand.Float64()-0.5)*4), round(y+(rand.Float64()-0.5)*4)); err != nil {
			return err
		}
	}
	// The cursor records the intended target, even after an overshoot correction.
	c.x, c.y = x, y
	return nil
}

type point struct{ X, Y float64 }

func ease(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}

func bezier(a, b, c, d point, t float64) point {
	u := 1 - t
	return point{u*u*u*a.X + 3*u*u*t*b.X + 3*u*t*t*c.X + t*t*t*d.X, u*u*u*a.Y + 3*u*u*t*b.Y + 3*u*t*t*c.Y + t*t*t*d.Y}
}

func (c *Cursor) rawMove(x, y float64) error {
	buttons := 0
	if c.buttonDown {
		buttons = 1
	}
	params := map[string]any{"type": "mouseMoved", "x": x, "y": y, "buttons": buttons}
	if m := c.modifiers(); m != 0 {
		params["modifiers"] = m
	}
	if _, err := c.cdp.Send("Input.dispatchMouseEvent", params); err != nil {
		return err
	}
	c.rawX, c.rawY = x, y
	return nil
}

func (c *Cursor) mouseDown() error {
	c.buttonDown = true
	params := map[string]any{"type": "mousePressed", "button": "left", "buttons": 1, "x": c.rawX, "y": c.rawY, "clickCount": 1}
	if m := c.modifiers(); m != 0 {
		params["modifiers"] = m
	}
	_, err := c.cdp.Send("Input.dispatchMouseEvent", params)
	return err
}

func (c *Cursor) mouseUp() error {
	params := map[string]any{"type": "mouseReleased", "button": "left", "x": c.rawX, "y": c.rawY, "clickCount": 1}
	if m := c.modifiers(); m != 0 {
		params["modifiers"] = m
	}
	if _, err := c.cdp.Send("Input.dispatchMouseEvent", params); err != nil {
		return err
	}
	c.buttonDown = false
	return nil
}

// viewport is the page's layout viewport, read over CDP: Playwright has no
// viewport size when the window decides it.
func (c *Cursor) viewport() (width, height float64, err error) {
	raw, err := c.cdp.Send("Page.getLayoutMetrics", nil)
	if err != nil {
		return 0, 0, err
	}
	metrics, _ := raw.(map[string]any)
	layout, _ := metrics["cssLayoutViewport"].(map[string]any)
	width, _ = layout["clientWidth"].(float64)
	height, _ = layout["clientHeight"].(float64)
	if width <= 0 || height <= 0 {
		return 0, 0, errors.New("human: no viewport size")
	}
	return width, height, nil
}

func (c *Cursor) inZone(b box, height float64) bool {
	return b.Y >= height*c.cfg.ScrollTargetZone[0] && b.Y+b.Height <= height*c.cfg.ScrollTargetZone[1]
}

func (c *Cursor) wheel(ctx context.Context, delta float64) error {
	sent, sign := 0.0, 1.0
	if delta < 0 {
		sign = -1
	}
	for sent < math.Abs(delta) {
		chunk := min(random(20, 40), math.Abs(delta)-sent)
		params := map[string]any{"type": "mouseWheel", "x": c.rawX, "y": c.rawY, "deltaX": 0, "deltaY": round(chunk) * sign}
		if m := c.modifiers(); m != 0 {
			params["modifiers"] = m
		}
		if _, err := c.cdp.Send("Input.dispatchMouseEvent", params); err != nil {
			return err
		}
		sent += chunk
		if err := sleep(ctx, random(8, 20)); err != nil {
			return err
		}
	}
	return nil
}

// scrollTo wheels the element into the comfortable zone in accelerating and
// decelerating steps, sometimes overshooting and coming back.
func (c *Cursor) scrollTo(ctx context.Context, l playwright.Locator) (box, error) {
	width, height, err := c.viewport()
	if err != nil {
		return box{}, err
	}
	b, err := elementBox(l)
	if err != nil {
		if err = sleep(ctx, 200); err != nil {
			return box{}, err
		}
		if b, err = elementBox(l); err != nil {
			return box{}, err
		}
	}
	if c.inZone(b, height) {
		return b, nil
	}
	cfg := c.cfg
	if err = c.move(ctx, round(width*random(0.3, 0.7)), round(height*random(0.3, 0.7))); err != nil {
		return box{}, err
	}
	if err = sleep(ctx, rangeValue(cfg.ScrollPreMoveDelay)); err != nil {
		return box{}, err
	}
	distance := b.Y + b.Height/2 - height*rangeValue(cfg.ScrollTargetZone)
	direction := 1.0
	if distance < 0 {
		direction = -1
	}
	if err = c.scrollSteps(ctx, l, height, math.Abs(distance), direction); err != nil {
		return box{}, err
	}
	if rand.Float64() < cfg.ScrollOvershootChance {
		if err = c.scrollOvershoot(ctx, direction); err != nil {
			return box{}, err
		}
	}
	if err = sleep(ctx, rangeValue(cfg.ScrollSettleDelay)); err != nil {
		return box{}, err
	}
	return elementBox(l)
}

// scrollSteps wheels about the distance in accelerating, steady and
// decelerating steps, stopping once the element is in the zone.
func (c *Cursor) scrollSteps(ctx context.Context, l playwright.Locator, height, distance, direction float64) error {
	cfg := c.cfg
	average := (cfg.ScrollDeltaBase[0] + cfg.ScrollDeltaBase[1]) / 2
	steps := int(max(3, math.Ceil(distance/average)))
	accel, decel := intRange(cfg.ScrollAccelSteps), intRange(cfg.ScrollDecelSteps)
	scrolled := 0.0
	for i := range steps {
		var delta, pause float64
		switch {
		case i < accel:
			delta, pause = random(80, 100), rangeValue(cfg.ScrollPauseSlow)
		case i >= steps-decel:
			delta, pause = random(60, 90), rangeValue(cfg.ScrollPauseSlow)
		default:
			delta, pause = rangeValue(cfg.ScrollDeltaBase), rangeValue(cfg.ScrollPauseFast)
		}
		delta = round(delta*(1+(rand.Float64()-0.5)*2*cfg.ScrollDeltaVariance)) * direction
		if err := c.wheel(ctx, delta); err != nil {
			return err
		}
		scrolled += math.Abs(delta)
		if err := sleep(ctx, pause); err != nil {
			return err
		}
		if i%3 == 2 || i == steps-1 {
			if b, err := elementBox(l); err == nil && c.inZone(b, height) {
				return nil
			}
		}
		if scrolled >= distance*1.1 {
			return nil
		}
	}
	return nil
}

// scrollOvershoot scrolls past the target and comes back in one or two moves.
func (c *Cursor) scrollOvershoot(ctx context.Context, direction float64) error {
	if err := c.wheel(ctx, round(rangeValue(c.cfg.ScrollOvershootPx))*direction); err != nil {
		return err
	}
	if err := sleep(ctx, rangeValue(c.cfg.ScrollSettleDelay)); err != nil {
		return err
	}
	for range intRange([2]float64{1, 2}) {
		if err := c.wheel(ctx, round(random(40, 80))*-direction); err != nil {
			return err
		}
		if err := sleep(ctx, random(100, 250)); err != nil {
			return err
		}
	}
	return nil
}

func random(lo, hi float64) float64   { return lo + rand.Float64()*(hi-lo) }
func rangeValue(r [2]float64) float64 { return random(r[0], r[1]) }
func intRange(r [2]float64) int       { return int(math.Floor(random(r[0], r[1]+1))) }
func round(v float64) float64         { return math.Floor(v + 0.5) }

// timeout is the time left before ctx's deadline, in milliseconds, for a
// Playwright call; nil leaves the context's default.
func timeout(ctx context.Context) *float64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return new(max(1, float64(time.Until(deadline).Milliseconds())))
}

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
