package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
)

func TestIterateAttributes(t *testing.T) {
	attrConfigs := []attributeConfig{
		{"one", 1},
		{"two", 2},
		{"three", 3},
	}

	expected := [][]attribute.KeyValue{
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000000"),
			attribute.String("three", "000000000"),
		},
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000000"),
			attribute.String("three", "000000001"),
		},
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000000"),
			attribute.String("three", "000000002"),
		},
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000001"),
			attribute.String("three", "000000000"),
		},
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000001"),
			attribute.String("three", "000000001"),
		},
		{
			attribute.String("one", "000000000"),
			attribute.String("two", "000000001"),
			attribute.String("three", "000000002"),
		},
	}

	got := slices.Collect(iterateAttributes(attrConfigs))
	assert.Equal(t, expected, got)
}
