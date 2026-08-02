package jump

import "testing"

func TestScoreEmptyQueryMatchesAll(t *testing.T) {
	if _, ok := Score("", "anything"); !ok {
		t.Fatal("empty query must match")
	}
	if _, ok := Score("   ", "anything"); !ok {
		t.Fatal("blank query must match")
	}
}

func TestScoreSubstring(t *testing.T) {
	s, ok := Score("ad30", "ad3009b4 worker reader")
	if !ok {
		t.Fatal("prefix of id must match")
	}
	scattered, ok2 := Score("ad30", "a-d-x-3-x-0")
	if !ok2 {
		t.Fatal("subsequence must match")
	}
	if s <= scattered {
		t.Fatalf("substring (%d) must outrank scattered subsequence (%d)", s, scattered)
	}
}

func TestScoreCaseInsensitive(t *testing.T) {
	a, ok := Score("READER", "02-00-adhoc reader|reader advisor")
	if !ok || a == 0 {
		t.Fatal("uppercase query must match lowercase text")
	}
}

func TestScoreMultiWordAll(t *testing.T) {
	if _, ok := Score("worker meridian", "d37083f4 worker 02-00-adhoc meridian-rs"); !ok {
		t.Fatal("both words present must match")
	}
	if _, ok := Score("worker zzz", "d37083f4 worker 02-00-adhoc"); ok {
		t.Fatal("one absent word must fail the whole query")
	}
}

func TestScoreBoundaryBonus(t *testing.T) {
	atBoundary, _ := Score("reader", "adhoc reader advisor")
	inside, _ := Score("reader", "xreaderx")
	if atBoundary <= inside {
		t.Fatalf("boundary match (%d) must outrank interior match (%d)", atBoundary, inside)
	}
}

func TestScoreNoMatch(t *testing.T) {
	if _, ok := Score("q7", "ad3009b4 worker"); ok {
		t.Fatal("absent characters must not match")
	}
}
