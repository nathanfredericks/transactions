package human

// config is the vendor's "careful" preset; delays are milliseconds unless noted.
type config struct {
	TypingDelay, TypingDelaySpread, TypingPauseChance                         float64
	TypingPauseRange, ShiftDownDelay, ShiftUpDelay, KeyHold, FieldSwitchDelay [2]float64
	MistypeChance                                                             float64
	MistypeDelayNotice, MistypeDelayCorrect                                   [2]float64
	MouseStepsDivisor, MouseMinSteps, MouseMaxSteps, MouseWobbleMax           float64
	MouseOvershootChance                                                      float64
	MouseOvershootPx, MouseBurstSize, MouseBurstPause                         [2]float64
	ClickAimDelayInput, ClickAimDelayButton, ClickHoldInput, ClickHoldButton  [2]float64
	ClickInputXRange                                                          [2]float64
	IdleDriftPx                                                               float64
	IdlePauseRange, ScrollDeltaBase                                           [2]float64
	ScrollDeltaVariance                                                       float64
	ScrollPauseFast, ScrollPauseSlow, ScrollAccelSteps, ScrollDecelSteps      [2]float64
	ScrollOvershootChance                                                     float64
	ScrollOvershootPx, ScrollSettleDelay, ScrollTargetZone                    [2]float64
	ScrollPreMoveDelay, InitialCursorX, InitialCursorY                        [2]float64
	IdleBetweenActions                                                        bool
	// IdleBetweenDuration is in seconds.
	IdleBetweenDuration [2]float64
}

var careful = config{
	TypingDelay: 100, TypingDelaySpread: 50, TypingPauseChance: 0.15, TypingPauseRange: [2]float64{500, 1200},
	ShiftDownDelay: [2]float64{40, 90}, ShiftUpDelay: [2]float64{30, 70}, KeyHold: [2]float64{20, 45},
	FieldSwitchDelay: [2]float64{1000, 2000},
	MistypeChance:    0.03, MistypeDelayNotice: [2]float64{150, 400}, MistypeDelayCorrect: [2]float64{80, 200},
	MouseStepsDivisor: 8, MouseMinSteps: 25, MouseMaxSteps: 80, MouseWobbleMax: 1.5,
	MouseOvershootChance: 0.1, MouseOvershootPx: [2]float64{3, 6}, MouseBurstSize: [2]float64{3, 5}, MouseBurstPause: [2]float64{12, 25},
	ClickAimDelayInput: [2]float64{80, 180}, ClickAimDelayButton: [2]float64{120, 280},
	ClickHoldInput: [2]float64{60, 140}, ClickHoldButton: [2]float64{80, 200}, ClickInputXRange: [2]float64{0.05, 0.3},
	IdleDriftPx: 3, IdlePauseRange: [2]float64{300, 1000},
	ScrollDeltaBase: [2]float64{80, 130}, ScrollDeltaVariance: 0.2,
	ScrollPauseFast: [2]float64{100, 200}, ScrollPauseSlow: [2]float64{250, 600},
	ScrollAccelSteps: [2]float64{2, 3}, ScrollDecelSteps: [2]float64{2, 3},
	ScrollOvershootChance: 0.1, ScrollOvershootPx: [2]float64{50, 150}, ScrollSettleDelay: [2]float64{400, 800},
	ScrollTargetZone: [2]float64{0.2, 0.8}, ScrollPreMoveDelay: [2]float64{150, 400},
	InitialCursorX: [2]float64{400, 700}, InitialCursorY: [2]float64{45, 60},
	IdleBetweenActions: true, IdleBetweenDuration: [2]float64{0.4, 1},
}
