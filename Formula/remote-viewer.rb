# Formula is auto-updated by the release CI workflow.
# To install manually before the first CI run:
#
#   brew tap yacovdroridev/remote_screen_with_rotatation
#   brew install remote-viewer

class RemoteViewer < Formula
  desc "High-performance SSH remote desktop viewer for Raspberry Pi"
  homepage "https://github.com/yacovdroridev/remote_screen_with_rotatation"
  version "2.1.0"
  url "https://github.com/yacovdroridev/remote_screen_with_rotatation/releases/download/v2.1.0/remote_viewer_macos"
  sha256 "placeholder-updated-by-ci"

  def install
    bin.install "remote_viewer_macos" => "remote-viewer"
  end

  def caveats
    <<~EOS
      Launch with:
        remote-viewer

      The viewer opens in your default browser.
      Use Chrome/Chromium for the best app-mode experience.
    EOS
  end

  test do
    assert_predicate bin/"remote-viewer", :exist?
  end
end
