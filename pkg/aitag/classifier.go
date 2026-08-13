package aitag

import "context"

// DescribeCap is the first pass of a describe-then-classify provider.
type DescribeCap interface {
	Describe(ctx context.Context, frame Frame) (string, error)
}

// VerifyCap is the second pass of a describe-then-classify provider.
type VerifyCap interface {
	Verify(ctx context.Context, frame Frame, labels []string) (map[string]bool, error)
}

// Classifier supports both passes of open-vocabulary frame classification.
type Classifier interface {
	DescribeCap
	VerifyCap
}
