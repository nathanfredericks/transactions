// Derived from CloakBrowser 0.3.25 (MIT); see third_party/cloakbrowser-0.3.25/LICENSE.
package human

import (
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"math/rand/v2"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

var nearby = map[rune]string{'a': "sqwz", 'b': "vghn", 'c': "xdfv", 'd': "sfecx", 'e': "wrsdf", 'f': "dgrtcv", 'g': "fhtyb", 'h': "gjybn", 'i': "ujko", 'j': "hkunm", 'k': "jloi", 'l': "kop", 'm': "njk", 'n': "bhjm", 'o': "iklp", 'p': "ol", 'q': "wa", 'r': "edft", 's': "awedxz", 't': "rfgy", 'u': "yhji", 'v': "cfgb", 'w': "qase", 'x': "zsdc", 'y': "tghu", 'z': "asx", '1': "2q", '2': "13qw", '3': "24we", '4': "35er", '5': "46rt", '6': "57ty", '7': "68yu", '8': "79ui", '9': "80io", '0': "9p"}
var symbolCodes = map[rune]string{'!': "Digit1", '@': "Digit2", '#': "Digit3", '$': "Digit4", '%': "Digit5", '^': "Digit6", '&': "Digit7", '*': "Digit8", '(': "Digit9", ')': "Digit0", '_': "Minus", '+': "Equal", '{': "BracketLeft", '}': "BracketRight", '|': "Backslash", ':': "Semicolon", '"': "Quote", '<': "Comma", '>': "Period", '?': "Slash", '~': "Backquote"}
var symbolVK = map[rune]int{'!': 49, '@': 50, '#': 51, '$': 52, '%': 53, '^': 54, '&': 55, '*': 56, '(': 57, ')': 48, '_': 189, '+': 187, '{': 219, '}': 221, '|': 220, ':': 186, '"': 222, '<': 188, '>': 190, '?': 191, '~': 192}

func (p *Page) modifiers() int {
	n := 0
	for k := range p.state.keys {
		n |= k.Modifier()
	}
	return n
}
func (p *Page) down(k input.Key) error {
	p.state.keys[k] = true
	ev := k.Encode(proto.InputDispatchKeyEventTypeKeyDown, p.modifiers())
	if k == input.Enter {
		ev.Key = "Enter"
	}
	if k == input.Tab {
		ev.Key = "Tab"
	}
	if p.modifiers()&7 != 0 {
		ev.Text = ""
		ev.UnmodifiedText = ""
	}
	return ev.Call(p.state.raw)
}
func (p *Page) up(k input.Key) error {
	delete(p.state.keys, k)
	ev := k.Encode(proto.InputDispatchKeyEventTypeKeyUp, p.modifiers())
	if k == input.Enter {
		ev.Key = "Enter"
	}
	if k == input.Tab {
		ev.Key = "Tab"
	}
	ev.Text = ""
	ev.UnmodifiedText = ""
	return ev.Call(p.state.raw)
}
func (p *Page) key(k input.Key) error {
	if err := p.down(k); err != nil {
		return err
	}
	return p.up(k)
}
func (p *Page) normal(ch rune) error {
	k, err := characterKey(ch)
	if err != nil {
		return err
	}
	if err := p.down(k); err != nil {
		return err
	}
	if err := sleep(p.ctx(), rangeValue(p.cfg.KeyHold)); err != nil {
		return err
	}
	return p.up(k)
}
func (p *Page) shifted(ch rune, symbol bool) error {
	if err := p.down(input.ShiftLeft); err != nil {
		return err
	}
	if err := sleep(p.ctx(), rangeValue(p.cfg.ShiftDownDelay)); err != nil {
		return err
	}
	if symbol {
		ev := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyDown, Modifiers: 8, Key: string(ch), Code: symbolCodes[ch], WindowsVirtualKeyCode: symbolVK[ch], Text: string(ch), UnmodifiedText: string(ch)}
		p.state.symbol = &ev
		if err := ev.Call(p.state.raw); err != nil {
			return err
		}
		if err := sleep(p.ctx(), rangeValue(p.cfg.KeyHold)); err != nil {
			return err
		}
		ev.Type = proto.InputDispatchKeyEventTypeKeyUp
		ev.Text = ""
		ev.UnmodifiedText = ""
		if err := ev.Call(p.state.raw); err != nil {
			return err
		}
		p.state.symbol = nil
	} else {
		if err := p.normal(ch); err != nil {
			return err
		}
	}
	if err := sleep(p.ctx(), rangeValue(p.cfg.ShiftUpDelay)); err != nil {
		return err
	}
	return p.up(input.ShiftLeft)
}
func (p *Page) interChar() error {
	if rand.Float64() < p.cfg.TypingPauseChance {
		return sleep(p.ctx(), rangeValue(p.cfg.TypingPauseRange))
	}
	return sleep(p.ctx(), max(10, p.cfg.TypingDelay+(rand.Float64()-0.5)*2*p.cfg.TypingDelaySpread))
}
func (p *Page) typeText(text string) error {
	chars := []rune(text)
	for i, ch := range chars {
		if ch >= 128 {
			if err := sleep(p.ctx(), rangeValue(p.cfg.KeyHold)); err != nil {
				return err
			}
			if err := p.state.raw.InsertText(string(ch)); err != nil {
				return err
			}
		} else {
			if rand.Float64() < p.cfg.MistypeChance && (ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
				neighbors := nearby[unicode.ToLower(ch)]
				wrong := ch
				if neighbors != "" {
					wrong = rune(neighbors[rand.IntN(len(neighbors))])
					if ch >= 'A' && ch <= 'Z' {
						wrong = unicode.ToUpper(wrong)
					}
				}
				// Matches reference: typo path uses normal key handling, even for uppercase.
				if err := p.normal(wrong); err != nil {
					return err
				}
				if err := sleep(p.ctx(), rangeValue(p.cfg.MistypeDelayNotice)); err != nil {
					return err
				}
				if err := p.down(input.Backspace); err != nil {
					return err
				}
				if err := sleep(p.ctx(), rangeValue(p.cfg.KeyHold)); err != nil {
					return err
				}
				if err := p.up(input.Backspace); err != nil {
					return err
				}
				if err := sleep(p.ctx(), rangeValue(p.cfg.MistypeDelayCorrect)); err != nil {
					return err
				}
			}
			var err error
			if ch >= 'A' && ch <= 'Z' {
				err = p.shifted(ch, false)
			} else if _, ok := symbolCodes[ch]; ok {
				err = p.shifted(ch, true)
			} else {
				err = p.normal(ch)
			}
			if err != nil {
				return err
			}
		}
		if i < len(chars)-1 {
			if err := p.interChar(); err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *Page) shortcut(key string) error { return p.shortcutHold(key, 0) }
func (p *Page) shortcutHold(key string, hold float64) error {
	// Browser host determines select-all, as in the pinned wrapper.
	var keys []input.Key
	for _, part := range strings.Split(key, "+") {
		switch part {
		case "ControlOrMeta":
			if runtime.GOOS == "darwin" {
				keys = append(keys, input.MetaLeft)
			} else {
				keys = append(keys, input.ControlLeft)
			}
		case "Control", "Ctrl", "ControlLeft":
			keys = append(keys, input.ControlLeft)
		case "Meta", "MetaLeft":
			keys = append(keys, input.MetaLeft)
		case "Shift", "ShiftLeft":
			keys = append(keys, input.ShiftLeft)
		case "Alt", "AltLeft":
			keys = append(keys, input.AltLeft)
		case "Enter":
			keys = append(keys, input.Enter)
		case "Tab":
			keys = append(keys, input.Tab)
		case "Backspace":
			keys = append(keys, input.Backspace)
		case "Escape":
			keys = append(keys, input.Escape)
		case "ArrowDown":
			keys = append(keys, input.ArrowDown)
		case "ArrowUp":
			keys = append(keys, input.ArrowUp)
		case "ArrowLeft":
			keys = append(keys, input.ArrowLeft)
		case "ArrowRight":
			keys = append(keys, input.ArrowRight)
		case "Delete":
			keys = append(keys, input.Delete)
		case "Home":
			keys = append(keys, input.Home)
		case "End":
			keys = append(keys, input.End)
		case "PageUp":
			keys = append(keys, input.PageUp)
		case "PageDown":
			keys = append(keys, input.PageDown)
		case "Space":
			keys = append(keys, input.Space)
		case "ControlRight":
			keys = append(keys, input.ControlRight)
		case "MetaRight":
			keys = append(keys, input.MetaRight)
		case "AltRight":
			keys = append(keys, input.AltRight)
		case "ShiftRight":
			keys = append(keys, input.ShiftRight)
		case "CapsLock":
			keys = append(keys, input.CapsLock)
		case "Insert":
			keys = append(keys, input.Insert)
		case "ContextMenu":
			keys = append(keys, input.ContextMenu)
		default:
			if strings.HasPrefix(part, "F") {
				n, err := strconv.Atoi(strings.TrimPrefix(part, "F"))
				if err == nil && n >= 1 && n <= 12 {
					keys = append(keys, input.F1+input.Key(n-1))
					continue
				}
			}
			r := []rune(part)
			if len(r) != 1 {
				return ErrKey
			}
			k, err := characterKey(r[0])
			if err != nil {
				return err
			}
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if err := p.down(k); err != nil {
			return err
		}
	}
	if err := sleep(p.ctx(), hold); err != nil {
		return err
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if err := p.up(keys[i]); err != nil {
			return err
		}
	}
	return nil
}
func (p *Page) selectAll() error {
	if runtime.GOOS == "darwin" {
		return p.shortcut("Meta+a")
	}
	return p.shortcut("Control+a")
}

func characterKey(ch rune) (input.Key, error) {
	switch ch {
	case '\n', '\r':
		return input.Enter, nil
	case '\t':
		return input.Tab, nil
	default:
		if ch < 32 || ch > 126 {
			return 0, ErrKey
		}
		return input.Key(ch), nil
	}
}
