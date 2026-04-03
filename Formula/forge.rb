class Forge < Formula
  desc "Silent memory layer for AI coding agents"
  homepage "https://github.com/intelogroup/forgememory-cli"
  
  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/intelogroup/forgememory-cli/releases/download/v0.3.6/forge-darwin-arm64.tar.gz"
      sha256 "586861c2c6953d1fa4bc4e3e283f356650fb81c3a028b7d892a8ad8e12fd7031"
    else
      url "https://github.com/intelogroup/forgememory-cli/releases/download/v0.3.6/forge-darwin-amd64.tar.gz"
      sha256 "128db818953affd657b19bd77e1b2d93e84c1ddfd12503c2b26e50135ce2db23"
    end
  elsif OS.linux?
    if Hardware::CPU.arm64?
      url "https://github.com/intelogroup/forgememory-cli/releases/download/v0.3.6/forge-linux-arm64.tar.gz"
      sha256 "d975c2bbc03ef3f3ccfcd3f409ae60f7da426b1b8c37b692f008cae5f31962d3"
    else
      url "https://github.com/intelogroup/forgememory-cli/releases/download/v0.3.6/forge-linux-amd64.tar.gz"
      sha256 "6f2443b19aaa66284ec84ab0b2effda4d0254746f3eda3b14dcc9c715b033d5f"
    end
  end

  version "0.3.6"

  def install
    bin.install "forge"
  end

  test do
    assert_match "forge", shell_output("#{bin}/forge version")
  end
end