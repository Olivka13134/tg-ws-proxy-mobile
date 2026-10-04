import AVFoundation

/// iOS suspends apps a few seconds after they leave the screen, which would
/// kill the local proxy while Telegram is open. An app that plays audio is
/// kept running, so we loop one second of silence, mixed with other sound
/// (music and Telegram's own audio keep playing normally).
final class BackgroundKeepAlive {
    var onResume: (() -> Void)?

    private var player: AVAudioPlayer?
    private var wanted = false
    private var observers: [NSObjectProtocol] = []

    init() {
        let nc = NotificationCenter.default
        observers.append(nc.addObserver(forName: AVAudioSession.interruptionNotification,
                                        object: nil, queue: .main) { [weak self] note in
            self?.handleInterruption(note)
        })
        observers.append(nc.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification,
                                        object: nil, queue: .main) { [weak self] _ in
            guard let self = self, self.wanted else { return }
            self.player = nil
            self.play()
        })
    }

    deinit {
        observers.forEach { NotificationCenter.default.removeObserver($0) }
    }

    func start() {
        wanted = true
        play()
    }

    func stop() {
        wanted = false
        player?.stop()
        player = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
    }

    private func play() {
        guard wanted else { return }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .default, options: [.mixWithOthers])
            try session.setActive(true)
            if player == nil {
                let p = try AVAudioPlayer(data: Self.silentWAV())
                p.numberOfLoops = -1
                p.volume = 0.01
                p.prepareToPlay()
                player = p
            }
            if player?.isPlaying == false {
                player?.play()
            }
        } catch {
            NSLog("TgWsProxy keep-alive: %@", error.localizedDescription)
        }
    }

    private func handleInterruption(_ note: Notification) {
        guard let info = note.userInfo,
              let raw = info[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        if type == .ended {
            // Phone call over, Siri done, etc.: resume silence and the proxy.
            play()
            onResume?()
        }
    }

    /// 1 s of 16-bit mono silence at 8 kHz as an in-memory WAV file.
    private static func silentWAV() -> Data {
        let sampleRate: UInt32 = 8000
        let samples = Int(sampleRate)
        let dataSize = UInt32(samples * 2)
        var d = Data()
        func u32(_ v: UInt32) { var x = v.littleEndian; d.append(Data(bytes: &x, count: 4)) }
        func u16(_ v: UInt16) { var x = v.littleEndian; d.append(Data(bytes: &x, count: 2)) }
        d.append(contentsOf: Array("RIFF".utf8)); u32(36 + dataSize)
        d.append(contentsOf: Array("WAVE".utf8))
        d.append(contentsOf: Array("fmt ".utf8)); u32(16); u16(1); u16(1)
        u32(sampleRate); u32(sampleRate * 2); u16(2); u16(16)
        d.append(contentsOf: Array("data".utf8)); u32(dataSize)
        d.append(Data(count: Int(dataSize)))
        return d
    }
}
