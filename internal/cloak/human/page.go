// Derived from CloakBrowser 0.3.25 (MIT); see third_party/cloakbrowser-0.3.25/LICENSE.
package human

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"math"
	"sync"
	"time"
)

var ErrKey = errors.New("unsupported keyboard shortcut")
var ErrElement = errors.New("element unavailable")

type cursor struct {
	mu                      sync.Mutex
	raw                     *rod.Page
	x, y, rawX, rawY        float64
	initialized, buttonDown bool
	keys                    map[input.Key]bool
	symbol                  *proto.InputDispatchKeyEvent
}
type Page struct {
	Raw   *rod.Page
	cfg   Config
	state *cursor
	world *worldState
}
type worldState struct {
	mu sync.Mutex
	id proto.RuntimeExecutionContextID
}

type Element struct {
	Raw  *rod.Element
	page *Page
}

func NewPage(raw *rod.Page, cfg Config) (*Page, error) {
	p := &Page{Raw: raw, cfg: cfg, world: &worldState{}, state: &cursor{raw: raw, keys: map[input.Key]bool{}}}
	if err := p.initCursor(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *Page) ctx() context.Context { return p.Raw.GetContext() }

// Context clones the wrapper while sharing cursor ownership. Operations use the
// caller's deadline rather than Rod's original page deadline for all CDP input.
func (p *Page) Context(ctx context.Context) *Page {
	return &Page{Raw: p.Raw.Context(ctx), cfg: p.cfg, state: p.state, world: p.world}
}
func (p *Page) action(fn func() error) (err error) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	previous := p.state.raw
	p.state.raw = previous.Context(p.ctx())
	defer func() {
		if err != nil {
			p.cleanupInput()
		}
		p.state.raw = previous
	}()
	return fn()
}
func (p *Page) cleanupInput() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw := p.state.raw.Context(ctx)
	if ev := p.state.symbol; ev != nil {
		copy := *ev
		copy.Type = proto.InputDispatchKeyEventTypeKeyUp
		copy.Text = ""
		copy.UnmodifiedText = ""
		_ = copy.Call(raw)
		p.state.symbol = nil
	}
	// Rod Keyboard owns its original page. Dispatch cleanup directly to the clone.
	for key := range p.state.keys {
		ev := key.Encode(proto.InputDispatchKeyEventTypeKeyUp, 0)
		ev.Text, ev.UnmodifiedText = "", ""
		_ = ev.Call(raw)
		delete(p.state.keys, key)
	}
	if p.state.buttonDown {
		_ = (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, Button: proto.InputMouseButtonLeft, X: p.state.rawX, Y: p.state.rawY, ClickCount: 1}).Call(raw)
		p.state.buttonDown = false
	}
}
func (p *Page) Navigate(url string) error {
	err := p.Raw.Navigate(url)
	p.world.mu.Lock()
	p.world.id = 0
	p.world.mu.Unlock()
	return err
}

// Evaluate executes DOM reads in an isolated world. Navigation invalidation is
// handled by recreating the world once, as in the source wrapper.
func (p *Page) Evaluate(expression string) (*proto.RuntimeRemoteObject, error) {
	p.world.mu.Lock()
	defer p.world.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if p.world.id == 0 {
			frame := p.Raw.FrameID
			if frame == "" {
				tree, e := (proto.PageGetFrameTree{}).Call(p.Raw)
				if e != nil {
					return nil, e
				}
				frame = tree.FrameTree.Frame.ID
			}
			result, e := (proto.PageCreateIsolatedWorld{FrameID: frame, GrantUniveralAccess: true}).Call(p.Raw)
			if e != nil {
				return nil, e
			}
			p.world.id = result.ExecutionContextID
		}
		result, e := (proto.RuntimeEvaluate{Expression: expression, ContextID: p.world.id, ReturnByValue: true}).Call(p.Raw)
		if e == nil && result.ExceptionDetails == nil {
			return result.Result, nil
		}
		p.world.id = 0
	}
	return nil, ErrElement
}
func (p *Page) query(selector, label string) (*rod.Element, error) {
	// The selection world is isolated too; Rod's default Evaluate uses its utility
	// world but ElementByJS does not expose a chosen context ID.
	args, _ := json.Marshal([]string{selector, label})
	expression := `((selector,label)=>Array.from(document.querySelectorAll(selector)).find(el=>el.getClientRects().length&&getComputedStyle(el).visibility!=="hidden"&&(!label||(!el.disabled&&el.getAttribute("aria-disabled")!=="true"&&getComputedStyle(el).pointerEvents!=="none"&&new RegExp(label).test((el.getAttribute("aria-label")||el.innerText||el.textContent).trim()))))||null)(...` + string(args) + `)`
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			return nil, ErrElement
		case <-p.ctx().Done():
			return nil, p.ctx().Err()
		default:
		}
		p.world.mu.Lock()
		if p.world.id == 0 {
			p.world.mu.Unlock()
			if _, err := p.Evaluate("true"); err != nil {
				if p.ctx().Err() != nil {
					return nil, p.ctx().Err()
				}
				if err = sleep(p.ctx(), 50); err != nil {
					return nil, err
				}
				continue
			}
			p.world.mu.Lock()
		}
		result, err := (proto.RuntimeEvaluate{Expression: expression, ContextID: p.world.id, ReturnByValue: false}).Call(p.Raw)
		if err == nil && result.ExceptionDetails == nil && result.Result.ObjectID != "" {
			// Resolve the node into Rod's utility world through its backend ID. This
			// keeps handle methods compatible without querying the main world.
			node, e := (proto.DOMDescribeNode{ObjectID: result.Result.ObjectID}).Call(p.Raw)
			_ = (proto.RuntimeReleaseObject{ObjectID: result.Result.ObjectID}).Call(p.Raw)
			p.world.mu.Unlock()
			if e != nil {
				return nil, e
			}
			return p.Raw.ElementFromNode(&proto.DOMNode{BackendNodeID: node.Node.BackendNodeID})
		}
		if err != nil || result.ExceptionDetails != nil {
			p.world.id = 0
		}
		p.world.mu.Unlock()
		select {
		case <-p.ctx().Done():
			return nil, p.ctx().Err()
		case <-deadline.C:
			return nil, ErrElement
		default:
		}
		if err := sleep(p.ctx(), 50); err != nil {
			return nil, err
		}
	}
}
func (p *Page) Element(selector string) (*Element, error) {
	raw, err := p.query(selector, "")
	if err != nil {
		return nil, err
	}
	return &Element{raw, p}, nil
}
func (p *Page) ElementLabel(selector, label string) (*Element, error) {
	raw, err := p.query(selector, label)
	if err != nil {
		return nil, err
	}
	return &Element{raw, p}, nil
}
func (p *Page) Wrap(raw *rod.Element) *Element { return &Element{raw, p} }
func (p *Page) Frame(el *rod.Element) (*Page, error) {
	raw, err := el.Frame()
	if err != nil {
		return nil, err
	}
	return &Page{Raw: raw, cfg: p.cfg, state: p.state, world: &worldState{}}, nil
}
func (e *Element) Element(selector string) (*Element, error) {
	raw, err := e.Raw.Element(selector)
	if err != nil {
		return nil, err
	}
	return e.page.Wrap(raw), nil
}
func (e *Element) Elements(selector string) ([]*Element, error) {
	raw, err := e.Raw.Elements(selector)
	if err != nil {
		return nil, err
	}
	result := make([]*Element, 0, len(raw))
	for _, el := range raw {
		result = append(result, e.page.Wrap(el))
	}
	return result, nil
}
func (e *Element) box() (Box, error) {
	shape, err := (proto.DOMGetBoxModel{ObjectID: e.Raw.Object.ObjectID}).Call(e.Raw)
	if err != nil {
		return Box{}, err
	}
	if shape.Model == nil || len(shape.Model.Border) == 0 {
		return Box{}, ErrElement
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, quad := range []proto.DOMQuad{shape.Model.Border} {
		for i := 0; i < len(quad); i += 2 {
			minX = min(minX, quad[i])
			minY = min(minY, quad[i+1])
			maxX = max(maxX, quad[i])
			maxY = max(maxY, quad[i+1])
		}
	}
	if maxX <= minX || maxY <= minY {
		return Box{}, ErrElement
	}
	return Box{minX, minY, maxX - minX, maxY - minY}, nil
}
func (e *Element) isInput() (bool, error) {
	result, err := e.Raw.Eval(`()=>{const tag=this.tagName.toLowerCase();return tag==="input"||tag==="textarea"||this.getAttribute("contenteditable")==="true"}`)
	if err != nil {
		return false, err
	}
	return result.Value.Bool(), nil
}
func (e *Element) focused() (bool, error) {
	v, err := e.Raw.Eval(`()=>this===document.activeElement`)
	if err != nil {
		return false, err
	}
	return v.Value.Bool(), nil
}
func (e *Element) moveTo(scroll bool) (bool, error) {
	p := e.page
	if err := p.beforeMove(); err != nil {
		return false, err
	}
	var box Box
	var err error
	if scroll {
		box, err = p.scrollTo(e)
	} else {
		box, err = e.box()
	}
	if err != nil {
		return false, err
	}
	isInput, err := e.isInput()
	if err != nil {
		return false, err
	}
	target := p.target(box, isInput)
	return isInput, p.move(target.X, target.Y)
}
func (e *Element) click(scroll bool) error {
	isInput, err := e.moveTo(scroll)
	if err != nil {
		return err
	}
	return e.page.clickMouse(isInput)
}
func (e *Element) Click() error { return e.page.action(func() error { return e.click(false) }) }
func (e *Element) DoubleClick() error {
	return e.page.action(func() error {
		if _, err := e.moveTo(false); err != nil {
			return err
		}
		if err := e.page.mouseDown(2); err != nil {
			return err
		}
		if err := sleep(e.page.ctx(), random(30, 60)); err != nil {
			return err
		}
		return e.page.mouseUp(2)
	})
}
func (e *Element) Hover() error {
	return e.page.action(func() error { _, err := e.moveTo(false); return err })
}
func (e *Element) Tap() error { return e.Click() }
func (e *Element) Focus() error {
	return e.page.action(func() error {
		if _, err := e.moveTo(false); err != nil {
			return err
		}
		return e.Raw.Focus()
	})
}
func (e *Element) typeOrFill(value string, fill bool, pageStyle bool) error {
	p := e.page
	if pageStyle {
		if err := sleep(p.ctx(), rangeValue(p.cfg.FieldSwitchDelay)); err != nil {
			return err
		}
	}
	if err := e.click(pageStyle); err != nil {
		return err
	}
	if err := sleep(p.ctx(), random(100, 250)); err != nil {
		return err
	}
	if fill {
		if err := p.selectAll(); err != nil {
			return err
		}
		if err := sleep(p.ctx(), random(30, 80)); err != nil {
			return err
		}
		if err := p.key(input.Backspace); err != nil {
			return err
		}
		if err := sleep(p.ctx(), random(50, 150)); err != nil {
			return err
		}
	}
	return p.typeText(value)
}
func (e *Element) Type(value string) error {
	return e.page.action(func() error { return e.typeOrFill(value, false, false) })
}
func (e *Element) Fill(value string) error {
	return e.page.action(func() error { return e.typeOrFill(value, true, false) })
}
func (e *Element) Clear() error { return e.page.action(func() error { return e.clear() }) }
func (e *Element) clear() error {
	p := e.page
	focused, err := e.focused()
	if err != nil {
		return err
	}
	if !focused {
		if err = e.click(true); err != nil {
			return err
		}
	}
	if err = sleep(p.ctx(), random(50, 150)); err != nil {
		return err
	}
	if err = p.selectAll(); err != nil {
		return err
	}
	if err = sleep(p.ctx(), random(30, 80)); err != nil {
		return err
	}
	return p.key(input.Backspace)
}
func (e *Element) Press(key string) error {
	return e.page.action(func() error {
		if err := sleep(e.page.ctx(), random(20, 60)); err != nil {
			return err
		}
		return e.page.shortcutHold(key, rangeValue(e.page.cfg.KeyHold))
	})
}
func (e *Element) SetChecked(wanted bool) error {
	return e.page.action(func() error {
		v, err := e.Raw.Property("checked")
		if err != nil {
			return err
		}
		if v.Bool() == wanted {
			return nil
		}
		return e.click(false)
	})
}
func (e *Element) Check() error   { return e.SetChecked(true) }
func (e *Element) Uncheck() error { return e.SetChecked(false) }
func (e *Element) SelectOption(values []string) error {
	return e.page.action(func() error {
		if err := e.click(false); err != nil {
			return err
		}
		if err := sleep(e.page.ctx(), random(100, 300)); err != nil {
			return err
		}
		return e.selectValues(values)
	})
}
func (p *Page) Click(selector, label string) error {
	return p.action(func() error {
		e, err := p.ElementLabel(selector, label)
		if err != nil {
			return err
		}
		return e.click(true)
	})
}
func (p *Page) Hover(selector string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		_, err = e.moveTo(true)
		return err
	})
}
func (p *Page) Fill(selector, value string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		return e.typeOrFill(value, true, true)
	})
}
func (p *Page) Type(selector, value string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		return e.typeOrFill(value, false, true)
	})
}
func (p *Page) PressSequentially(selector, value string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		focused, err := e.focused()
		if err != nil {
			return err
		}
		if !focused {
			if err = e.click(true); err != nil {
				return err
			}
		}
		if err = sleep(p.ctx(), random(100, 250)); err != nil {
			return err
		}
		return p.typeText(value)
	})
}
func (p *Page) Press(selector, key string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		focused, err := e.focused()
		if err != nil {
			return err
		}
		if !focused {
			if err = e.click(true); err != nil {
				return err
			}
		}
		if err = sleep(p.ctx(), random(50, 150)); err != nil {
			return err
		}
		return p.shortcut(key)
	})
}
func (p *Page) Clear(selector string) error {
	e, err := p.Element(selector)
	if err != nil {
		return err
	}
	return e.Clear()
}
func (p *Page) DoubleClick(selector string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		if _, err = e.moveTo(true); err != nil {
			return err
		}
		if err = p.mouseDown(2); err != nil {
			return err
		}
		if err = sleep(p.ctx(), random(30, 60)); err != nil {
			return err
		}
		return p.mouseUp(2)
	})
}
func (p *Page) Tap(selector string) error { return p.Click(selector, "") }
func (p *Page) SetChecked(selector string, wanted bool) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		if p.cfg.IdleBetweenActions {
			if err = p.idle(rangeValue(p.cfg.IdleBetweenDuration)); err != nil {
				return err
			}
		}
		v, err := e.Raw.Property("checked")
		if err != nil {
			return err
		}
		if v.Bool() == wanted {
			return nil
		}
		return e.click(true)
	})
}
func (p *Page) SelectOption(selector string, values []string) error {
	return p.action(func() error {
		e, err := p.Element(selector)
		if err != nil {
			return err
		}
		if _, err = e.moveTo(true); err != nil {
			return err
		}
		if err = sleep(p.ctx(), random(100, 300)); err != nil {
			return err
		}
		return e.selectValues(values)
	})
}
func (p *Page) Drag(source, target string) error {
	return p.action(func() error {
		src, err := p.Element(source)
		if err != nil {
			return err
		}
		dst, err := p.Element(target)
		if err != nil {
			return err
		}
		a, err := src.box()
		if err != nil {
			return err
		}
		b, err := dst.box()
		if err != nil {
			return err
		}
		if err = p.move(a.X+a.Width/2, a.Y+a.Height/2); err != nil {
			return err
		}
		if err = sleep(p.ctx(), random(100, 200)); err != nil {
			return err
		}
		if err = p.mouseDown(1); err != nil {
			return err
		}
		if err = sleep(p.ctx(), random(80, 150)); err != nil {
			return err
		}
		if err = p.move(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
			return err
		}
		if err = sleep(p.ctx(), random(80, 150)); err != nil {
			return err
		}
		return p.mouseUp(1)
	})
}
func (p *Page) Move(x, y float64) error { return p.action(func() error { return p.move(x, y) }) }
func (p *Page) MouseClick(x, y float64) error {
	return p.action(func() error {
		if err := p.move(x, y); err != nil {
			return err
		}
		return p.clickMouse(false)
	})
}
func (p *Page) KeyboardType(text string) error {
	return p.action(func() error { return p.typeText(text) })
}

// SelectOption strings match option values, as in Playwright. Use SelectLabel
// or SelectIndex for its structured label/index forms.
func (e *Element) selectValues(values []string) error {
	selectors := make([]string, 0, len(values))
	for _, value := range values {
		quoted, _ := json.Marshal(value)
		selectors = append(selectors, "[value="+string(quoted)+"]")
	}
	return e.Raw.Select(selectors, true, rod.SelectorTypeCSSSector)
}
func (e *Element) SelectLabel(labels []string) error {
	return e.page.action(func() error {
		if err := e.click(false); err != nil {
			return err
		}
		if err := sleep(e.page.ctx(), random(100, 300)); err != nil {
			return err
		}
		return e.Raw.Select(labels, true, rod.SelectorTypeText)
	})
}
func (e *Element) SelectIndex(index int) error {
	if index < 0 {
		return ErrElement
	}
	return e.page.action(func() error {
		if err := e.click(false); err != nil {
			return err
		}
		if err := sleep(e.page.ctx(), random(100, 300)); err != nil {
			return err
		}
		return e.Raw.Select([]string{fmt.Sprintf("option:nth-child(%d)", index+1)}, true, rod.SelectorTypeCSSSector)
	})
}
func (p *Page) Check(selector string) error   { return p.SetChecked(selector, true) }
func (p *Page) Uncheck(selector string) error { return p.SetChecked(selector, false) }
func (p *Page) Elements(selector string) ([]*Element, error) {
	raw, err := p.Raw.Elements(selector)
	if err != nil {
		return nil, err
	}
	result := make([]*Element, 0, len(raw))
	for _, el := range raw {
		result = append(result, p.Wrap(el))
	}
	return result, nil
}
func (p *Page) KeyboardPress(key string) error {
	return p.action(func() error { return p.shortcut(key) })
}
func (p *Page) KeyboardInsertText(text string) error {
	return p.action(func() error { return p.state.raw.InsertText(text) })
}
