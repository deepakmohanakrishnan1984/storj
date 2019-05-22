// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package cmd

import (
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"storj.io/storj/internal/testcontext"
	"storj.io/storj/pkg/storj"
)

func TestSaveEncryptionKey(t *testing.T) {
	generateInputKey := func() []byte {
		inputKey := make([]byte, rand.Intn(storj.KeySize*3)+1)
		_, err := rand.Read(inputKey)
		require.NoError(t, err)

		return inputKey
	}

	t.Run("ok", func(t *testing.T) {
		ctx := testcontext.New(t)
		defer ctx.Cleanup()

		inputKey := generateInputKey()
		filename := ctx.File("storj-test-cmd-uplink", "encryption.key")
		err := saveEncryptionKey(inputKey, filename)
		require.NoError(t, err)

		savedKey, err := ioutil.ReadFile(filename)
		require.NoError(t, err)

		assert.Equal(t, inputKey, savedKey)
	})

	t.Run("error: empty input key", func(t *testing.T) {
		ctx := testcontext.New(t)
		defer ctx.Cleanup()

		filename := ctx.File("storj-test-cmd-uplink", "encryption.key")

		err := saveEncryptionKey(nil, filename)
		require.Error(t, err)

		err = saveEncryptionKey([]byte{}, filename)
		require.Error(t, err)
	})

	t.Run("error: empty filepath", func(t *testing.T) {
		inputKey := generateInputKey()

		err := saveEncryptionKey(inputKey, "")
		require.Error(t, err)
	})

	t.Run("error: unexisting dir", func(t *testing.T) {
		// Create a directory and remove it for making sure that the path doesn't
		// exist
		ctx := testcontext.New(t)
		dir := ctx.Dir("storj-test-cmd-uplink")
		ctx.Cleanup()

		inputKey := generateInputKey()
		filename := filepath.Join(dir, "enc.key")
		err := saveEncryptionKey(inputKey, filename)
		require.Errorf(t, err, "directory path doesn't exist")
	})

	t.Run("error: file already exists", func(t *testing.T) {
		ctx := testcontext.New(t)
		defer ctx.Cleanup()

		inputKey := generateInputKey()
		filename := ctx.File("encryption.key")
		require.NoError(t, ioutil.WriteFile(filename, nil, os.FileMode(0600)))

		err := saveEncryptionKey(inputKey, filename)
		require.Errorf(t, err, "file key already exists")
	})
}
