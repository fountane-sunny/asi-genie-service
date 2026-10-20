package main

import (
	"reflect"
	"testing"
)

func TestCommaFilterValues(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want []string
	}{
		{
			name: "payment methods",
			desc: "Comma-separated payment methods filter (creditCard, debitCard, cash, check, badDebts, businessSource). Note: businessSource is accepted but ignored in filtering.",
			want: []string{"creditCard", "debitCard", "cash", "check", "badDebts", "businessSource"},
		},
		{
			name: "reservation status",
			desc: "Comma-separated reservation status filter (IN_HOUSE, CHECKED_OUT, CONFIRMED, UNCONFIRMED, TRANSFER_OUT, CANCELLED, NO_SHOW)",
			want: []string{"IN_HOUSE", "CHECKED_OUT", "CONFIRMED", "UNCONFIRMED", "TRANSFER_OUT", "CANCELLED", "NO_SHOW"},
		},
		{
			name: "comma-separated prefix inside the parens is stripped",
			desc: "Reservation type filter (comma-separated: rooms, spaces). Note: spaces returns empty data for now.",
			want: []string{"rooms", "spaces"},
		},
		{
			name: "no comma-separated phrase at all",
			desc: "Comma-separated config user IDs (from config service) to filter by desk user who recorded the payment",
			want: nil, // "(from config service)" isn't an enum; no "filter (" immediately before it
		},
		{
			name: "non-exhaustive enum (etc.) is rejected",
			desc: "Comma-separated card type filter (Visa, Mastercard, etc.)",
			want: nil,
		},
		{
			name: "date hint is not an enum",
			desc: "Comma-separated date filter (YYYY-MM-DD, defaults to current date)",
			want: nil,
		},
		{
			name: "single value is not a filter list",
			desc: "Comma-separated filter (onlyOne)",
			want: nil,
		},
		{
			name: "no comma-separated marker",
			desc: "Status filter (Completed, Failed, Declined, etc.)",
			want: nil,
		},
		{
			name: "no description",
			desc: "",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := commaFilterValues(c.desc)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("commaFilterValues(%q) = %v, want %v", c.desc, got, c.want)
			}
		})
	}
}
