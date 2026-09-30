class HelmForge < Formula
  desc "Fast drop-in replacement for helm dependency build/update (OCI only)"
  homepage "https://github.com/mmpyro/forge"
  version "1.0.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/mmpyro/forge/releases/download/v#{version}/forge-darwin-arm64"
      sha256 "3c265982a45755be424485c9b157fde9c6cb58ff41d3fddb97c5b5bfa158e1e4"
    end
    on_intel do
      url "https://github.com/mmpyro/forge/releases/download/v#{version}/forge-darwin-amd64"
      sha256 "10e9080f4c5926b15f9729cf8abdad64295d6510fabf92b6800df84c0eac642d"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/mmpyro/forge/releases/download/v#{version}/forge-linux-arm64"
      sha256 "743baf7034553476da7d09fb5d365b551a8efc216aa41161fc7ea4a04632ec3d"
    end
    on_intel do
      url "https://github.com/mmpyro/forge/releases/download/v#{version}/forge-linux-amd64"
      sha256 "a00262cbac416d5f9261747c2fcb1c3f4bb7148dc3d31ec9775e9020462df0f5"
    end
  end

  def install
    bin.install Dir["forge-*"].first => "forge"
  end

  test do
    assert_match "forge #{version}", shell_output("#{bin}/forge --version")
  end
end
