// magi-vz runs macOS task VMs with Apple's Virtualization framework.
//
//   magi-vz install --ipsw FILE --dir DIR [--disk-gb N] [--cpus N] [--memory-gb N]
//       Installs macOS from a restore image into DIR (disk.img, aux.img,
//       vm.json). DIR is the template of the task VMs.
//   magi-vz run --dir DIR --sockets SOCKDIR [--share NAME=PATH[:ro] ...]
//       Starts the VM in DIR with no network card. The shares are one
//       virtio-fs device with the macOS automount tag: the guest sees
//       /Volumes/My Shared Files/NAME. vsock works like the hybrid vsock of
//       Cloud Hypervisor: a host program connects to SOCKDIR/vsock.sock and
//       writes "CONNECT <port>\n" (answer "OK <port>\n"); a guest connection
//       to host port P goes to the unix socket SOCKDIR/vsock.sock_P.
//
// The binary needs the entitlement com.apple.security.virtualization
// (tools/vm/build-macos-vz.sh signs it).

import Foundation
import Virtualization

struct Settings: Codable {
    var cpus: Int
    var memoryGB: UInt64
    var hardwareModel: String  // base64
    var machineIdentifier: String  // base64
}

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(("magi-vz: " + msg + "\n").data(using: .utf8)!)
    exit(1)
}

func arg(_ name: String, _ args: [String]) -> String? {
    guard let i = args.firstIndex(of: "--" + name), i + 1 < args.count else { return nil }
    return args[i + 1]
}

func args(_ name: String, _ args: [String]) -> [String] {
    var out: [String] = []
    var i = 0
    while i < args.count {
        if args[i] == "--" + name, i + 1 < args.count {
            out.append(args[i + 1])
            i += 1
        }
        i += 1
    }
    return out
}

func configuration(dir: URL, settings s: Settings) -> VZVirtualMachineConfiguration {
    let platform = VZMacPlatformConfiguration()
    guard let hwData = Data(base64Encoded: s.hardwareModel), let hw = VZMacHardwareModel(dataRepresentation: hwData),
        let idData = Data(base64Encoded: s.machineIdentifier), let id = VZMacMachineIdentifier(dataRepresentation: idData)
    else { fail("vm.json: bad hardware model or machine identifier") }
    platform.hardwareModel = hw
    platform.machineIdentifier = id
    platform.auxiliaryStorage = VZMacAuxiliaryStorage(contentsOf: dir.appendingPathComponent("aux.img"))
    let c = VZVirtualMachineConfiguration()
    c.platform = platform
    c.bootLoader = VZMacOSBootLoader()
    c.cpuCount = s.cpus
    c.memorySize = s.memoryGB * 1024 * 1024 * 1024
    do {
        let disk = try VZDiskImageStorageDeviceAttachment(url: dir.appendingPathComponent("disk.img"), readOnly: false)
        c.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: disk)]
    } catch { fail("disk: \(error)") }
    let g = VZMacGraphicsDeviceConfiguration()
    g.displays = [VZMacGraphicsDisplayConfiguration(widthInPixels: 1920, heightInPixels: 1080, pixelsPerInch: 80)]
    c.graphicsDevices = [g]
    c.keyboards = [VZUSBKeyboardConfiguration()]
    c.pointingDevices = [VZUSBScreenCoordinatePointingDeviceConfiguration()]
    c.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    // No network card: the guest reaches the host only over vsock.
    c.networkDevices = []
    return c
}

// MARK: install

func install(_ a: [String]) {
    guard let ipsw = arg("ipsw", a), let dirPath = arg("dir", a) else { fail("install --ipsw FILE --dir DIR") }
    let dir = URL(fileURLWithPath: dirPath)
    let diskGB = UInt64(arg("disk-gb", a) ?? "100") ?? 100
    let cpus = Int(arg("cpus", a) ?? "4") ?? 4
    let memGB = UInt64(arg("memory-gb", a) ?? "8") ?? 8
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    VZMacOSRestoreImage.load(from: URL(fileURLWithPath: ipsw)) { result in
        let image: VZMacOSRestoreImage
        switch result {
        case .success(let i): image = i
        case .failure(let e): fail("restore image: \(e)")
        }
        guard let req = image.mostFeaturefulSupportedConfiguration, req.hardwareModel.isSupported else {
            fail("this host cannot run macOS \(image.operatingSystemVersion.majorVersion).\(image.operatingSystemVersion.minorVersion)")
        }
        DispatchQueue.main.async {
            do {
                _ = try VZMacAuxiliaryStorage(creatingStorageAt: dir.appendingPathComponent("aux.img"), hardwareModel: req.hardwareModel, options: [])
                let disk = dir.appendingPathComponent("disk.img")
                FileManager.default.createFile(atPath: disk.path, contents: nil)
                let h = try FileHandle(forWritingTo: disk)
                try h.truncate(atOffset: diskGB * 1024 * 1024 * 1024)
                try h.close()
                let s = Settings(cpus: max(cpus, req.minimumSupportedCPUCount), memoryGB: max(memGB, req.minimumSupportedMemorySize / (1024 * 1024 * 1024)),
                    hardwareModel: req.hardwareModel.dataRepresentation.base64EncodedString(),
                    machineIdentifier: VZMacMachineIdentifier().dataRepresentation.base64EncodedString())
                try JSONEncoder().encode(s).write(to: dir.appendingPathComponent("vm.json"))
                let c = configuration(dir: dir, settings: s)
                try c.validate()
                let vm = VZVirtualMachine(configuration: c)
                let inst = VZMacOSInstaller(virtualMachine: vm, restoringFromImageAt: URL(fileURLWithPath: ipsw))
                var last = -1
                let obs = inst.progress.observe(\.fractionCompleted) { p, _ in
                    let pct = Int(p.fractionCompleted * 100)
                    if pct / 5 != last / 5 {
                        last = pct
                        print("install \(pct)%")
                        fflush(stdout)
                    }
                }
                inst.install { r in
                    _ = obs
                    switch r {
                    case .success:
                        print("installed")
                        exit(0)
                    case .failure(let e): fail("install: \(e)")
                    }
                }
            } catch { fail("\(error)") }
        }
    }
    dispatchMain()
}

// MARK: run

/// Copies bytes between two file descriptors until both directions end.
func splice(_ a: Int32, _ b: Int32, done: @escaping () -> Void) {
    let group = DispatchGroup()
    for (src, dst) in [(a, b), (b, a)] {
        group.enter()
        Thread.detachNewThread {
            var buf = [UInt8](repeating: 0, count: 65536)
            while true {
                let n = read(src, &buf, buf.count)
                if n <= 0 { break }
                var off = 0
                while off < n {
                    let w = buf.withUnsafeBytes { write(dst, $0.baseAddress! + off, n - off) }
                    if w <= 0 { break }
                    off += w
                }
                if off < n { break }
            }
            shutdown(dst, SHUT_WR)
            group.leave()
        }
    }
    group.notify(queue: .global()) { done() }
}

func unixAddr(_ path: String) -> sockaddr_un {
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8)
    if bytes.count >= MemoryLayout.size(ofValue: addr.sun_path) { fail("socket path too long: \(path)") }
    withUnsafeMutableBytes(of: &addr.sun_path) { p in
        for (i, c) in bytes.enumerated() { p[i] = c }
    }
    return addr
}

func unixConnect(_ path: String) -> Int32 {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    var addr = unixAddr(path)
    let r = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) } }
    if r != 0 {
        close(fd)
        return -1
    }
    return fd
}

func unixListen(_ path: String) -> Int32 {
    unlink(path)
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    var addr = unixAddr(path)
    let r = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) } }
    if r != 0 || listen(fd, 64) != 0 { fail("listen \(path): \(String(cString: strerror(errno)))") }
    return fd
}

/// Guest connections to host ports go to SOCKDIR/vsock.sock_PORT.
final class Listener: NSObject, VZVirtioSocketListenerDelegate {
    let sockets: String
    let port: UInt32
    init(sockets: String, port: UInt32) {
        self.sockets = sockets
        self.port = port
    }
    func listener(_ l: VZVirtioSocketListener, shouldAcceptNewConnection c: VZVirtioSocketConnection, from d: VZVirtioSocketDevice) -> Bool {
        let up = unixConnect("vsock.sock_\(port)")
        if up < 0 { return false }
        let down = dup(c.fileDescriptor)
        splice(down, up) {
            close(down)
            close(up)
            _ = c  // keep the connection until the copy ends
        }
        return true
    }
}

final class Delegate: NSObject, VZVirtualMachineDelegate {
    func guestDidStop(_ vm: VZVirtualMachine) {
        print("the guest stopped")
        exit(0)
    }
    func virtualMachine(_ vm: VZVirtualMachine, didStopWithError e: Error) { fail("the VM stopped: \(e)") }
}

var keep: [AnyObject] = []

func run(_ a: [String]) {
    guard let dirPath = arg("dir", a), let sockets = arg("sockets", a) else { fail("run --dir DIR --sockets SOCKDIR") }
    let dir = URL(fileURLWithPath: dirPath)
    guard let data = try? Data(contentsOf: dir.appendingPathComponent("vm.json")), let s = try? JSONDecoder().decode(Settings.self, from: data) else {
        fail("read \(dir.path)/vm.json")
    }
    // Socket paths are relative to SOCKDIR, so that they stay short.
    guard FileManager.default.changeCurrentDirectoryPath(sockets) else { fail("cd \(sockets)") }
    let c = configuration(dir: dir, settings: s)
    var shares: [String: VZSharedDirectory] = [:]
    for spec in args("share", a) {
        guard let eq = spec.firstIndex(of: "=") else { fail("--share NAME=PATH[:ro]") }
        var path = String(spec[spec.index(after: eq)...])
        let ro = path.hasSuffix(":ro")
        if ro { path.removeLast(3) }
        shares[String(spec[..<eq])] = VZSharedDirectory(url: URL(fileURLWithPath: path), readOnly: ro)
    }
    if !shares.isEmpty {
        let fs = VZVirtioFileSystemDeviceConfiguration(tag: VZVirtioFileSystemDeviceConfiguration.macOSGuestAutomountTag)
        fs.share = VZMultipleDirectoryShare(directories: shares)
        c.directorySharingDevices = [fs]
    }
    c.socketDevices = [VZVirtioSocketDeviceConfiguration()]
    do { try c.validate() } catch { fail("configuration: \(error)") }
    let vm = VZVirtualMachine(configuration: c)
    let del = Delegate()
    vm.delegate = del
    keep.append(del)
    signal(SIGTERM, SIG_IGN)
    let term = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
    term.setEventHandler {
        if vm.canRequestStop { try? vm.requestStop() }
        DispatchQueue.main.asyncAfter(deadline: .now() + 20) { vm.stop { _ in exit(0) } }
    }
    term.resume()
    keep.append(term)
    vm.start { r in
        if case .failure(let e) = r { fail("start: \(e)") }
        guard let dev = vm.socketDevices.first as? VZVirtioSocketDevice else { fail("no vsock device") }
        for port in args("host-port", a).compactMap({ UInt32($0) }) {
            let l = VZVirtioSocketListener()
            let d = Listener(sockets: sockets, port: port)
            l.delegate = d
            keep.append(d)
            keep.append(l)
            dev.setSocketListener(l, forPort: port)
        }
        // Host programs reach guest ports through vsock.sock.
        let lfd = unixListen("vsock.sock")
        Thread.detachNewThread {
            while true {
                let cfd = accept(lfd, nil, nil)
                if cfd < 0 { continue }
                var line: [UInt8] = []
                var b: UInt8 = 0
                while line.count < 64, read(cfd, &b, 1) == 1, b != 10 { line.append(b) }
                let text = String(decoding: line, as: UTF8.self)
                guard text.hasPrefix("CONNECT "), let port = UInt32(text.dropFirst(8)) else {
                    close(cfd)
                    continue
                }
                DispatchQueue.main.async {
                    dev.connect(toPort: port) { r in
                        switch r {
                        case .success(let conn):
                            let ok = "OK \(port)\n"
                            _ = ok.withCString { write(cfd, $0, strlen($0)) }
                            let gfd = dup(conn.fileDescriptor)
                            splice(cfd, gfd) {
                                close(cfd)
                                close(gfd)
                                _ = conn
                            }
                        case .failure:
                            close(cfd)
                        }
                    }
                }
            }
        }
        print("running")
        fflush(stdout)
    }
    dispatchMain()
}

let argv = Array(CommandLine.arguments.dropFirst())
switch argv.first {
case "install": install(Array(argv.dropFirst()))
case "run": run(Array(argv.dropFirst()))
default: fail("usage: magi-vz install|run ...")
}
