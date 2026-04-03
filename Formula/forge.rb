class Forge < Formula
  desc "Silent memory layer for AI coding agents"
  homepage "https://github.com/intelogroup/forgememory-cli"
  url "https://github.com/intelogroup/forgememory-cli/releases/download/v0.3.0/forge-darwin-amd64.tar.gz"
  version "0.3.0"
  sha256 "c90f988ad339bc9a0461e9896ebff43d2cf5dab9d833135e90b6a4c5c7394278"

  depends_on "node" => :optional

  def install
    if OS.mac?
      if Hardware::CPU.arm?
        bin.install "forge-darwin-arm64" => "forge"
      else
        bin.install "forge-darwin-amd64" => "forge"
      end
    elsif OS.linux?
      if Hardware::CPU.arm64?
        bin.install "forge-linux-arm64" => "forge"
      else
        bin.install "forge-linux-amd64" => "forge"
      end
    end
  end

  test do
    assert_match "forge", shell_output("#{bin}/forge version")
  end
end
