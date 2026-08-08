package tts

import "testing"

func TestPrepareTextPhoneDigits(t *testing.T) {
	got := PrepareText("Call 94741682210 now, order 123.", NumberModePhoneDigits)
	want := "Call 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 now, order 123."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPrepareTextAllDigits(t *testing.T) {
	got := PrepareText("PIN 1234", NumberModeAllDigits)
	want := "PIN 1, 2, 3, 4"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPrepareTextNatural(t *testing.T) {
	input := "94741682210 and 42"
	if got := PrepareText(input, NumberModeNatural); got != input {
		t.Fatalf("got %q", got)
	}
}
