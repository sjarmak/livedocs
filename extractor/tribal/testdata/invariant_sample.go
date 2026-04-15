package sample

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkThings(t TestingT) {
	err := doWork()
	require.NoError(t, err)
	require.Error(t, maybeFail())
	assert.True(t, isValid())
	assert.False(t, isBroken())
	if err != nil {
		panic("invariant violated")
	}
	//nolint:errcheck // intentional fire-and-forget
	_ = doWork()
}
