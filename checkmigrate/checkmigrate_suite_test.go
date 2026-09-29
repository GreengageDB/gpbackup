package checkmigrate_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCheckMigrate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "checkmigrate tests")
}
