package core

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/xs23933/uid"
)

func TestMakeDir(t *testing.T) {

	rel, abs, err := MakePath("favicon.png", "/images", "./static", uid.New(), true)
	assert.NoError(t, err)
	assert.NotEqual(t, rel, "")
	assert.NotEqual(t, abs, "")
	rel, abs, err = MakePath("favicon.png", "/images", "./static", uuid.New(), true)
	t.Log(rel, abs)
	t.Error("")
	assert.NoError(t, err)
	assert.NotEqual(t, rel, "")
	assert.NotEqual(t, abs, "")
}
