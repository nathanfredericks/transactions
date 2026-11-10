package human

import (
	"context"
	"errors"
	"math/rand/v2"
	"unicode"
	"unicode/utf8"
)

// keyInfo describes one physical key as CDP Input.dispatchKeyEvent expects it.
type keyInfo struct {
	key, code    string
	vk, location int
}

var (
	enter     = keyInfo{"\r", "Enter", 13, 0}
	tab       = keyInfo{"\t", "Tab", 9, 0}
	backspace = keyInfo{"Backspace", "Backspace", 8, 0}
	shift     = keyInfo{"Shift", "ShiftLeft", 16, 1}
	control   = keyInfo{"Control", "ControlLeft", 17, 1}
)

const (
	modifierControl = 2
	modifierShift   = 8
)

var errKey = errors.New("human: no key for character")

var (
	nearby      = map[rune]string{'a': "sqwz", 'b': "vghn", 'c': "xdfv", 'd': "sfecx", 'e': "wrsdf", 'f': "dgrtcv", 'g': "fhtyb", 'h': "gjybn", 'i': "ujko", 'j': "hkunm", 'k': "jloi", 'l': "kop", 'm': "njk", 'n': "bhjm", 'o': "iklp", 'p': "ol", 'q': "wa", 'r': "edft", 's': "awedxz", 't': "rfgy", 'u': "yhji", 'v': "cfgb", 'w': "qase", 'x': "zsdc", 'y': "tghu", 'z': "asx", '1': "2q", '2': "13qw", '3': "24we", '4': "35er", '5': "46rt", '6': "57ty", '7': "68yu", '8': "79ui", '9': "80io", '0': "9p"}
	symbolCodes = map[rune]string{'!': "Digit1", '@': "Digit2", '#': "Digit3", '$': "Digit4", '%': "Digit5", '^': "Digit6", '&': "Digit7", '*': "Digit8", '(': "Digit9", ')': "Digit0", '_': "Minus", '+': "Equal", '{': "BracketLeft", '}': "BracketRight", '|': "Backslash", ':': "Semicolon", '"': "Quote", '<': "Comma", '>': "Period", '?': "Slash", '~': "Backquote"}
	symbolVK    = map[rune]int{'!': 49, '@': 50, '#': 51, '$': 52, '%': 53, '^': 54, '&': 55, '*': 56, '(': 57, ')': 48, '_': 189, '+': 187, '{': 219, '}': 221, '|': 220, ':': 186, '"': 222, '<': 188, '>': 190, '?': 191, '~': 192}
	punctuation = map[rune]keyInfo{'-': {"-", "Minus", 189, 0}, '=': {"=", "Equal", 187, 0}, '[': {"[", "BracketLeft", 219, 0}, ']': {"]", "BracketRight", 221, 0}, '\\': {"\\", "Backslash", 220, 0}, ';': {";", "Semicolon", 186, 0}, '\'': {"'", "Quote", 222, 0}, ',': {",", "Comma", 188, 0}, '.': {".", "Period", 190, 0}, '/': {"/", "Slash", 191, 0}, '`': {"`", "Backquote", 192, 0}, ' ': {" ", "Space", 32, 0}}
)

// characterKey is the US-layout key that types ch.
func characterKey(ch rune) (keyInfo, error) {
	switch {
	case ch == '\n' || ch == '\r':
		return enter, nil
	case ch == '\t':
		return tab, nil
	case ch >= 'a' && ch <= 'z':
		u := unicode.ToUpper(ch)
		return keyInfo{string(ch), "Key" + string(u), int(u), 0}, nil
	case ch >= 'A' && ch <= 'Z':
		return keyInfo{string(ch), "Key" + string(ch), int(ch), 0}, nil
	case ch >= '0' && ch <= '9':
		return keyInfo{string(ch), "Digit" + string(ch), int(ch), 0}, nil
	}
	if k, ok := punctuation[ch]; ok {
		return k, nil
	}
	if code, ok := symbolCodes[ch]; ok {
		return keyInfo{string(ch), code, symbolVK[ch], 0}, nil
	}
	return keyInfo{}, errKey
}

// event encodes the key as Rod did: a printable key sends its text, others
// are raw key-downs.
func (k keyInfo) event(kind string, modifiers int) map[string]any {
	printable := utf8.RuneCountInString(k.key) == 1
	t := kind
	if kind == "keyDown" && !printable {
		t = "rawKeyDown"
	}
	ev := map[string]any{"type": t, "windowsVirtualKeyCode": k.vk, "code": k.code, "key": k.key, "location": k.location}
	if printable {
		ev["text"], ev["unmodifiedText"] = k.key, k.key
	}
	if modifiers != 0 {
		ev["modifiers"] = modifiers
	}
	return ev
}

func (c *Cursor) modifiers() int {
	n := 0
	for k := range c.keys {
		switch k.vk {
		case 17:
			n |= modifierControl
		case 16:
			n |= modifierShift
		}
	}
	return n
}

// named sets the key name CDP expects for Enter and Tab.
func named(ev map[string]any, k keyInfo) {
	switch k {
	case enter:
		ev["key"] = "Enter"
	case tab:
		ev["key"] = "Tab"
	}
}

func (c *Cursor) down(k keyInfo) error {
	c.keys[k] = true
	ev := k.event("keyDown", c.modifiers())
	named(ev, k)
	// With Control (or Alt or Meta) held, a key types nothing.
	if c.modifiers()&7 != 0 {
		delete(ev, "text")
		delete(ev, "unmodifiedText")
	}
	_, err := c.cdp.Send("Input.dispatchKeyEvent", ev)
	return err
}

func (c *Cursor) up(k keyInfo) error {
	delete(c.keys, k)
	ev := k.event("keyUp", c.modifiers())
	named(ev, k)
	delete(ev, "text")
	delete(ev, "unmodifiedText")
	_, err := c.cdp.Send("Input.dispatchKeyEvent", ev)
	return err
}

func (c *Cursor) key(ctx context.Context, k keyInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.down(k); err != nil {
		return err
	}
	return c.up(k)
}

func (c *Cursor) normal(ctx context.Context, ch rune) error {
	k, err := characterKey(ch)
	if err != nil {
		return err
	}
	if err = c.down(k); err != nil {
		return err
	}
	if err = sleep(ctx, rangeValue(c.cfg.KeyHold)); err != nil {
		return err
	}
	return c.up(k)
}

// shifted types an uppercase letter or a shifted symbol with Shift held.
// Symbols carry their own key and code, as the vendor's typeShiftSymbol does.
func (c *Cursor) shifted(ctx context.Context, ch rune, symbol bool) error {
	if err := c.down(shift); err != nil {
		return err
	}
	if err := sleep(ctx, rangeValue(c.cfg.ShiftDownDelay)); err != nil {
		return err
	}
	if symbol {
		ev := map[string]any{"type": "keyDown", "modifiers": modifierShift, "key": string(ch), "code": symbolCodes[ch], "windowsVirtualKeyCode": symbolVK[ch], "text": string(ch), "unmodifiedText": string(ch)}
		c.symbol = ev
		if _, err := c.cdp.Send("Input.dispatchKeyEvent", ev); err != nil {
			return err
		}
		if err := sleep(ctx, rangeValue(c.cfg.KeyHold)); err != nil {
			return err
		}
		up := map[string]any{"type": "keyUp", "modifiers": modifierShift, "key": string(ch), "code": symbolCodes[ch], "windowsVirtualKeyCode": symbolVK[ch]}
		if _, err := c.cdp.Send("Input.dispatchKeyEvent", up); err != nil {
			return err
		}
		c.symbol = nil
	} else if err := c.normal(ctx, ch); err != nil {
		return err
	}
	if err := sleep(ctx, rangeValue(c.cfg.ShiftUpDelay)); err != nil {
		return err
	}
	return c.up(shift)
}

func (c *Cursor) interChar(ctx context.Context) error {
	if rand.Float64() < c.cfg.TypingPauseChance {
		return sleep(ctx, rangeValue(c.cfg.TypingPauseRange))
	}
	return sleep(ctx, max(10, c.cfg.TypingDelay+(rand.Float64()-0.5)*2*c.cfg.TypingDelaySpread))
}

// typeText types each character with human timing, occasionally hitting a
// neighbouring key first and correcting it.
func (c *Cursor) typeText(ctx context.Context, text string) error {
	chars := []rune(text)
	for i, ch := range chars {
		if err := c.typeChar(ctx, ch); err != nil {
			return err
		}
		if i < len(chars)-1 {
			if err := c.interChar(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// typeChar types one character: non-ASCII as inserted text, the rest as keys.
func (c *Cursor) typeChar(ctx context.Context, ch rune) error {
	if ch >= 128 {
		if err := sleep(ctx, rangeValue(c.cfg.KeyHold)); err != nil {
			return err
		}
		_, err := c.cdp.Send("Input.insertText", map[string]any{"text": string(ch)})
		return err
	}
	if err := c.maybeMistype(ctx, ch); err != nil {
		return err
	}
	switch {
	case ch >= 'A' && ch <= 'Z':
		return c.shifted(ctx, ch, false)
	case symbolCodes[ch] != "":
		return c.shifted(ctx, ch, true)
	default:
		return c.normal(ctx, ch)
	}
}

func (c *Cursor) maybeMistype(ctx context.Context, ch rune) error {
	alnum := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
	if !alnum || rand.Float64() >= c.cfg.MistypeChance {
		return nil
	}
	wrong := ch
	if neighbors := nearby[unicode.ToLower(ch)]; neighbors != "" {
		wrong = rune(neighbors[rand.IntN(len(neighbors))])
		if ch >= 'A' && ch <= 'Z' {
			wrong = unicode.ToUpper(wrong)
		}
	}
	// As in the vendor, the typo goes through normal key handling, even uppercase.
	if err := c.normal(ctx, wrong); err != nil {
		return err
	}
	if err := sleep(ctx, rangeValue(c.cfg.MistypeDelayNotice)); err != nil {
		return err
	}
	if err := c.down(backspace); err != nil {
		return err
	}
	if err := sleep(ctx, rangeValue(c.cfg.KeyHold)); err != nil {
		return err
	}
	if err := c.up(backspace); err != nil {
		return err
	}
	return sleep(ctx, rangeValue(c.cfg.MistypeDelayCorrect))
}

// selectAll presses Control+A, the select-all shortcut on the Linux browser host.
func (c *Cursor) selectAll(ctx context.Context) error {
	a, _ := characterKey('a')
	if err := c.down(control); err != nil {
		return err
	}
	if err := c.down(a); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.up(a); err != nil {
		return err
	}
	return c.up(control)
}
