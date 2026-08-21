package models

import (
	"fmt"
	"io"
	"strconv"
)

type TranscodeBenefitEnum string

const (
	TranscodeBenefitHigh   TranscodeBenefitEnum = "HIGH"
	TranscodeBenefitMedium TranscodeBenefitEnum = "MEDIUM"
	TranscodeBenefitLow    TranscodeBenefitEnum = "LOW"
)

var AllTranscodeBenefitEnum = []TranscodeBenefitEnum{
	TranscodeBenefitHigh,
	TranscodeBenefitMedium,
	TranscodeBenefitLow,
}

func (e TranscodeBenefitEnum) IsValid() bool {
	switch e {
	case TranscodeBenefitHigh, TranscodeBenefitMedium, TranscodeBenefitLow:
		return true
	}
	return false
}

func (e TranscodeBenefitEnum) String() string {
	return string(e)
}

func (e *TranscodeBenefitEnum) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = TranscodeBenefitEnum(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid TranscodeBenefitEnum", str)
	}
	return nil
}

func (e TranscodeBenefitEnum) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}
