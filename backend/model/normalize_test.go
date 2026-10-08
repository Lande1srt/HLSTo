package model

import "testing"

func TestNormalizeMergeMethod(t *testing.T) {
	cases := map[string]string{
		"":        MergeMethodAuto,
		"auto":    MergeMethodAuto,
		"ffmpeg":  MergeMethodFFmpeg,
		"gomedia": MergeMethodGomedia,
		"unknown": MergeMethodAuto,
		// 严格匹配：大小写或首尾空白不一致均视为非法，回退默认
		"AUTO":     MergeMethodAuto,
		"FFmpeg":   MergeMethodAuto,
		"GOMEDIA":  MergeMethodAuto,
		" ffmpeg":  MergeMethodAuto,
		"gomedia ": MergeMethodAuto,
	}
	for in, want := range cases {
		if got := NormalizeMergeMethod(in); got != want {
			t.Errorf("NormalizeMergeMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeHLSEncryptMode(t *testing.T) {
	cases := map[string]string{
		"":          HLSEncryptGenerated,
		"generated": HLSEncryptGenerated,
		"specified": HLSEncryptSpecified,
		"bogus":     HLSEncryptGenerated,
		// 严格匹配
		"GENERATED":  HLSEncryptGenerated,
		"Specified":  HLSEncryptGenerated,
		" specified": HLSEncryptGenerated,
	}
	for in, want := range cases {
		if got := NormalizeHLSEncryptMode(in); got != want {
			t.Errorf("NormalizeHLSEncryptMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRetryMode(t *testing.T) {
	cases := map[RetryMode]RetryMode{
		"":                  RetryModeMissing,
		RetryModeMissing:    RetryModeMissing,
		RetryModeRedownload: RetryModeRedownload,
		RetryModeForceMerge: RetryModeForceMerge,
		"force":             RetryModeMissing,
		"FULL_REDOWNLOAD":   RetryModeMissing,
		"retry_missing ":    RetryModeMissing,
		"unknown":           RetryModeMissing,
	}
	for in, want := range cases {
		if got := NormalizeRetryMode(in); got != want {
			t.Errorf("NormalizeRetryMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeHLSPackForm(t *testing.T) {
	cases := map[string]string{
		"":        HLSPackFormMulti,
		"multi":   HLSPackFormMulti,
		"single":  HLSPackFormSingle,
		"bogus":   HLSPackFormMulti,
		"MULTI":   HLSPackFormMulti,
		"Single":  HLSPackFormMulti,
		" single": HLSPackFormMulti,
		"single ": HLSPackFormMulti,
	}
	for in, want := range cases {
		if got := NormalizeHLSPackForm(in); got != want {
			t.Errorf("NormalizeHLSPackForm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMuxMode(t *testing.T) {
	cases := map[string]string{
		"copy":    MuxModeCopy,
		"":        MuxModeCopy,
		"h264":    MuxModeH264,
		"h265":    MuxModeH265,
		"invalid": MuxModeCopy,
		// 严格匹配：大写与带空白均回退 copy
		"H264":  MuxModeCopy,
		"H265":  MuxModeCopy,
		" copy": MuxModeCopy,
		"copy ": MuxModeCopy,
	}
	for in, want := range cases {
		if got := NormalizeMuxMode(in); got != want {
			t.Errorf("NormalizeMuxMode(%q) = %q, want %q", in, got, want)
		}
	}
}
