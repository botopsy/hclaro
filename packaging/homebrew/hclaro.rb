# Source-build formula for a future botopsy/homebrew-tap (docs/RELEASING.md#homebrew-formula).
# It builds from the tag's source archive; the web UI is already committed under
# internal/ui/dist, so only Go is needed. Update url and sha256 together for every release.
class Hclaro < Formula
  desc "Local-first manager for Terraform and OpenTofu projects"
  homepage "https://github.com/botopsy/hclaro"
  url "https://github.com/botopsy/hclaro/archive/refs/tags/v0.1.0-beta.3.tar.gz"
  sha256 "c4f9a618e9fd81208c84dfc655519fdef756a6bac06de9abaaf8a05b9cec82d1"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    system "go", "build", "-buildvcs=false", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/hclaro"
  end

  test do
    assert_equal "hclaro #{version}", shell_output("#{bin}/hclaro --version").strip
  end
end
