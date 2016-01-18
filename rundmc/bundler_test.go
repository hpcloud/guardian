package rundmc_test

import (
	"os"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/cloudfoundry-incubator/goci"
	"github.com/cloudfoundry-incubator/goci/specs"
	"github.com/cloudfoundry-incubator/guardian/gardener"
	"github.com/cloudfoundry-incubator/guardian/rundmc"
	"github.com/cloudfoundry-incubator/guardian/rundmc/fakes"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
)

var _ = Describe("CompositeBundler", func() {
	var bundler rundmc.BundleTemplate

	Context("when there is only one rule", func() {
		var rule *fakes.FakeBundlerRule

		BeforeEach(func() {
			rule = new(fakes.FakeBundlerRule)
			bundler = rundmc.BundleTemplate{
				Rules: []rundmc.BundlerRule{rule},
			}
		})

		It("returns the bundle from the first rule", func() {
			returnedSpec := goci.Bndl{}.WithRootFS("something")
			rule.ApplyStub = func(bndle *goci.Bndl, spec gardener.DesiredContainerSpec) *goci.Bndl {
				Expect(spec.RootFSPath).To(Equal("the-rootfs"))
				return returnedSpec
			}

			result := bundler.Generate(gardener.DesiredContainerSpec{RootFSPath: "the-rootfs"})
			Expect(result).To(Equal(returnedSpec))
		})

		It("passes nil as the bundle to the first rule", func() {
			bundler.Generate(gardener.DesiredContainerSpec{})

			bndl, _ := rule.ApplyArgsForCall(0)
			Expect(bndl).To(BeNil())
		})
	})

	Context("with multiple rules", func() {
		var (
			ruleA, ruleB *fakes.FakeBundlerRule
		)

		BeforeEach(func() {
			ruleA = new(fakes.FakeBundlerRule)
			ruleB = new(fakes.FakeBundlerRule)

			bundler = rundmc.BundleTemplate{
				Rules: []rundmc.BundlerRule{
					ruleA, ruleB,
				},
			}
		})

		It("calls all the rules", func() {
			bundler.Generate(gardener.DesiredContainerSpec{})

			Expect(ruleA.ApplyCallCount()).To(Equal(1))
			Expect(ruleB.ApplyCallCount()).To(Equal(1))
		})

		It("passes the bundle from the first rule to the subsequent rules", func() {
			bndl := goci.Bndl{}.WithMounts(
				goci.Mount{Name: "test_a"},
				goci.Mount{Name: "test_b"},
			)
			ruleA.ApplyReturns(bndl)

			bundler.Generate(gardener.DesiredContainerSpec{})

			Expect(ruleB.ApplyCallCount()).To(Equal(1))
			recBndl, _ := ruleB.ApplyArgsForCall(0)
			Expect(recBndl).To(Equal(bndl))
		})

		It("returns the results of the last rule", func() {
			bndl := goci.Bndl{}.WithMounts(
				goci.Mount{Name: "test_a"},
				goci.Mount{Name: "test_b"},
			)
			ruleB.ApplyReturns(bndl)

			recBndl := bundler.Generate(gardener.DesiredContainerSpec{})
			Expect(recBndl).To(Equal(bndl))
		})
	})
})

var _ = Describe("BaseTemplateRule", func() {
	var (
		privilegeBndl, unprivilegeBndl *goci.Bndl

		rule rundmc.BaseTemplateRule
	)

	BeforeEach(func() {
		privilegeBndl = goci.Bndl{}.WithNamespace(goci.NetworkNamespace)
		unprivilegeBndl = goci.Bndl{}.WithNamespace(goci.UserNamespace)

		rule = rundmc.BaseTemplateRule{
			PrivilegedBase:   privilegeBndl,
			UnprivilegedBase: unprivilegeBndl,
		}
	})

	Context("when it is privileged", func() {
		It("should use the correct base", func() {
			retBndl := rule.Apply(nil, gardener.DesiredContainerSpec{
				Privileged: true,
			})

			Expect(retBndl).To(Equal(privilegeBndl))
		})
	})

	Context("when it is not privileged", func() {
		It("should use the correct base", func() {
			retBndl := rule.Apply(nil, gardener.DesiredContainerSpec{
				Privileged: false,
			})

			Expect(retBndl).To(Equal(unprivilegeBndl))
		})
	})
})

var _ = Describe("RootFSRule", func() {
	It("applies the rootfs to the passed bundle", func() {
		bndl := goci.Bndl{}.WithNamespace(goci.UserNamespace)

		newBndl := rundmc.RootFSRule{}.Apply(bndl, gardener.DesiredContainerSpec{RootFSPath: "/path/to/banana/rootfs"})
		Expect(newBndl.Spec.Root.Path).To(Equal("/path/to/banana/rootfs"))
	})
})

var _ = Describe("NetworkHookRule", func() {
	DescribeTable("the envirionment should contain", func(envVar string) {
		rule := rundmc.NetworkHookRule{LogFilePattern: "/path/to/%s.log"}

		newBndl := rule.Apply(goci.Bundle(), gardener.DesiredContainerSpec{
			Handle: "fred",
		})

		Expect(newBndl.RuntimeSpec.Hooks.Prestart[0].Env).To(
			ContainElement(envVar),
		)
	},
		Entry("the GARDEN_LOG_FILE path", "GARDEN_LOG_FILE=/path/to/fred.log"),
		Entry("a sensible PATH", "PATH="+os.Getenv("PATH")),
	)

	It("add the hook to the pre-start hooks of the passed bundle", func() {
		newBndl := rundmc.NetworkHookRule{}.Apply(goci.Bundle(), gardener.DesiredContainerSpec{
			NetworkHook: gardener.Hook{
				Path: "/path/to/bananas/network",
				Args: []string{"arg", "barg"},
			},
		})

		Expect(pathAndArgsOf(newBndl.RuntimeSpec.Hooks.Prestart)).To(ContainElement(PathAndArgs{
			Path: "/path/to/bananas/network",
			Args: []string{"arg", "barg"},
		}))
	})
})

func pathAndArgsOf(a []specs.Hook) (b []PathAndArgs) {
	for _, h := range a {
		b = append(b, PathAndArgs{h.Path, h.Args})
	}

	return
}

type PathAndArgs struct {
	Path string
	Args []string
}

var _ = Describe("BindMountsRule", func() {
	var newBndl *goci.Bndl

	BeforeEach(func() {
		newBndl = rundmc.BindMountsRule{}.Apply(goci.Bundle(), gardener.DesiredContainerSpec{
			BindMounts: []garden.BindMount{
				{
					SrcPath: "/path/to/ro/src",
					DstPath: "/path/to/ro/dest",
					Mode:    garden.BindMountModeRO,
				},
				{
					SrcPath: "/path/to/rw/src",
					DstPath: "/path/to/rw/dest",
					Mode:    garden.BindMountModeRW,
				},
			},
		})
	})

	It("adds mounts in the bundle spec", func() {
		Expect(newBndl.Spec.Mounts).To(HaveLen(2))
		Expect(newBndl.Spec.Mounts[0].Path).To(Equal("/path/to/ro/dest"))
		Expect(newBndl.Spec.Mounts[1].Path).To(Equal("/path/to/rw/dest"))
	})

	It("uses the same names for the mounts in the runtime spec", func() {
		mountAName := newBndl.Spec.Mounts[0].Name
		mountBName := newBndl.Spec.Mounts[1].Name

		Expect(newBndl.RuntimeSpec.Mounts).To(HaveKey(mountAName))
		Expect(newBndl.RuntimeSpec.Mounts).To(HaveKey(mountBName))
		Expect(newBndl.RuntimeSpec.Mounts[mountBName]).NotTo(Equal(newBndl.RuntimeSpec.Mounts[mountAName]))
	})

	It("sets the correct runtime spec mount options", func() {
		mountAName := newBndl.Spec.Mounts[0].Name
		mountBName := newBndl.Spec.Mounts[1].Name

		Expect(newBndl.RuntimeSpec.Mounts[mountAName]).To(Equal(specs.Mount{
			Type:    "bind",
			Source:  "/path/to/ro/src",
			Options: []string{"bind", "ro"},
		}))

		Expect(newBndl.RuntimeSpec.Mounts[mountBName]).To(Equal(specs.Mount{
			Type:    "bind",
			Source:  "/path/to/rw/src",
			Options: []string{"bind", "rw"},
		}))
	})
})
