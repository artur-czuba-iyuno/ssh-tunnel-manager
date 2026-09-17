export namespace config {
	
	export class ConfigFileInfo {
	    path: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigFileInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.label = source["label"];
	    }
	}
	export class LogEntry {
	    // Go type: time
	    timestamp: any;
	    level: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = this.convertValues(source["timestamp"], null);
	        this.level = source["level"];
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PortForward {
	    localPort: number;
	    remoteHost: string;
	    remotePort: number;
	    description: string;
	    portless?: boolean;
	    domain?: string;
	    exposePort?: number;
	    hostHeader?: string;
	    hostHeaderOn?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PortForward(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.localPort = source["localPort"];
	        this.remoteHost = source["remoteHost"];
	        this.remotePort = source["remotePort"];
	        this.description = source["description"];
	        this.portless = source["portless"];
	        this.domain = source["domain"];
	        this.exposePort = source["exposePort"];
	        this.hostHeader = source["hostHeader"];
	        this.hostHeaderOn = source["hostHeaderOn"];
	    }
	}
	export class TunnelConfig {
	    id: string;
	    name: string;
	    host: string;
	    port: number;
	    user: string;
	    keyPath: string;
	    portForwards: PortForward[];
	    proxyCommand?: string;
	    proxyJump?: string;
	    color?: string;
	    group: string;
	    autoConnect: boolean;
	    pinned: boolean;
	    sourceFile?: string;
	    sourceFileLabel?: string;
	
	    static createFrom(source: any = {}) {
	        return new TunnelConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.user = source["user"];
	        this.keyPath = source["keyPath"];
	        this.portForwards = this.convertValues(source["portForwards"], PortForward);
	        this.proxyCommand = source["proxyCommand"];
	        this.proxyJump = source["proxyJump"];
	        this.color = source["color"];
	        this.group = source["group"];
	        this.autoConnect = source["autoConnect"];
	        this.pinned = source["pinned"];
	        this.sourceFile = source["sourceFile"];
	        this.sourceFileLabel = source["sourceFileLabel"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {

	export class PortlessFallbackStatus {
	    tunnelId: string;
	    message: string;
	    recommendation?: string;
	    command?: string;

	    static createFrom(source: any = {}) {
	        return new PortlessFallbackStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tunnelId = source["tunnelId"];
	        this.message = source["message"];
	        this.recommendation = source["recommendation"];
	        this.command = source["command"];
	    }
	}
	export class PortlessServiceStatus {
	    available: boolean;
	    installed: boolean;
	    approvalRequired: boolean;
	    current: boolean;
	    state: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PortlessServiceStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.installed = source["installed"];
	        this.approvalRequired = source["approvalRequired"];
	        this.current = source["current"];
	        this.state = source["state"];
	        this.message = source["message"];
	    }
	}
	export class SFTPOpenResult {
	    sessionId: string;
	    home: string;
	
	    static createFrom(source: any = {}) {
	        return new SFTPOpenResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.home = source["home"];
	    }
	}
	export class SFTPUploadPick {
	    paths: string[];
	    conflicts: string[];
	
	    static createFrom(source: any = {}) {
	        return new SFTPUploadPick(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.paths = source["paths"];
	        this.conflicts = source["conflicts"];
	    }
	}
	export class SFTPWriteResult {
	    conflict: boolean;
	    // Go type: time
	    modTime: any;
	
	    static createFrom(source: any = {}) {
	        return new SFTPWriteResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conflict = source["conflict"];
	        this.modTime = this.convertValues(source["modTime"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}
export namespace ssh {
	
	export class FileEntry {
	    name: string;
	    path: string;
	    size: number;
	    mode: string;
	    isDir: boolean;
	    isLink: boolean;
	    // Go type: time
	    modTime: any;
	
	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.size = source["size"];
	        this.mode = source["mode"];
	        this.isDir = source["isDir"];
	        this.isLink = source["isLink"];
	        this.modTime = this.convertValues(source["modTime"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TextFileResult {
	    content: string;
	    // Go type: time
	    modTime: any;
	    size: number;
	    mode: string;
	    binary: boolean;
	    tooLarge: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TextFileResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.modTime = this.convertValues(source["modTime"], null);
	        this.size = source["size"];
	        this.mode = source["mode"];
	        this.binary = source["binary"];
	        this.tooLarge = source["tooLarge"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace sysstats {
	
	export class CPUCore {
	    name: string;
	    percent: number;
	
	    static createFrom(source: any = {}) {
	        return new CPUCore(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.percent = source["percent"];
	    }
	}
	export class Capabilities {
	    docker: boolean;
	    htop: boolean;
	    os: string;
	
	    static createFrom(source: any = {}) {
	        return new Capabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.docker = source["docker"];
	        this.htop = source["htop"];
	        this.os = source["os"];
	    }
	}
	export class DiskMount {
	    filesystem: string;
	    total: number;
	    used: number;
	    avail: number;
	    usePercent: number;
	    mountPoint: string;
	
	    static createFrom(source: any = {}) {
	        return new DiskMount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filesystem = source["filesystem"];
	        this.total = source["total"];
	        this.used = source["used"];
	        this.avail = source["avail"];
	        this.usePercent = source["usePercent"];
	        this.mountPoint = source["mountPoint"];
	    }
	}
	export class DockerContainer {
	    id: string;
	    name: string;
	    image: string;
	    status: string;
	    state: string;
	    ports: string;
	    cpuPercent: string;
	    memPercent: string;
	    memUsage: string;
	
	    static createFrom(source: any = {}) {
	        return new DockerContainer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.status = source["status"];
	        this.state = source["state"];
	        this.ports = source["ports"];
	        this.cpuPercent = source["cpuPercent"];
	        this.memPercent = source["memPercent"];
	        this.memUsage = source["memUsage"];
	    }
	}
	export class DockerMount {
	    type: string;
	    source: string;
	    destination: string;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new DockerMount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.source = source["source"];
	        this.destination = source["destination"];
	        this.mode = source["mode"];
	    }
	}
	export class DockerPortMapping {
	    containerPort: string;
	    hostIp: string;
	    hostPort: string;
	
	    static createFrom(source: any = {}) {
	        return new DockerPortMapping(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.containerPort = source["containerPort"];
	        this.hostIp = source["hostIp"];
	        this.hostPort = source["hostPort"];
	    }
	}
	export class DockerContainerDetails {
	    id: string;
	    name: string;
	    image: string;
	    state: string;
	    status: string;
	    created: string;
	    command: string;
	    restartCount: number;
	    ports: DockerPortMapping[];
	    mounts: DockerMount[];
	    networks: string[];
	    env: string[];
	    hasStats: boolean;
	    cpuPercent: string;
	    memUsage: string;
	    memPercent: string;
	    netIO: string;
	    blockIO: string;
	    pids: string;
	
	    static createFrom(source: any = {}) {
	        return new DockerContainerDetails(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.state = source["state"];
	        this.status = source["status"];
	        this.created = source["created"];
	        this.command = source["command"];
	        this.restartCount = source["restartCount"];
	        this.ports = this.convertValues(source["ports"], DockerPortMapping);
	        this.mounts = this.convertValues(source["mounts"], DockerMount);
	        this.networks = source["networks"];
	        this.env = source["env"];
	        this.hasStats = source["hasStats"];
	        this.cpuPercent = source["cpuPercent"];
	        this.memUsage = source["memUsage"];
	        this.memPercent = source["memPercent"];
	        this.netIO = source["netIO"];
	        this.blockIO = source["blockIO"];
	        this.pids = source["pids"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class ProcessInfo {
	    pid: number;
	    user: string;
	    cpu: number;
	    mem: number;
	    command: string;
	
	    static createFrom(source: any = {}) {
	        return new ProcessInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.user = source["user"];
	        this.cpu = source["cpu"];
	        this.mem = source["mem"];
	        this.command = source["command"];
	    }
	}
	export class ProcessStats {
	    cores: CPUCore[];
	    memTotal: number;
	    memUsed: number;
	    swapTotal: number;
	    swapUsed: number;
	    load1: number;
	    load5: number;
	    load15: number;
	    uptimeSeconds: number;
	    processes: ProcessInfo[];
	
	    static createFrom(source: any = {}) {
	        return new ProcessStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cores = this.convertValues(source["cores"], CPUCore);
	        this.memTotal = source["memTotal"];
	        this.memUsed = source["memUsed"];
	        this.swapTotal = source["swapTotal"];
	        this.swapUsed = source["swapUsed"];
	        this.load1 = source["load1"];
	        this.load5 = source["load5"];
	        this.load15 = source["load15"];
	        this.uptimeSeconds = source["uptimeSeconds"];
	        this.processes = this.convertValues(source["processes"], ProcessInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ServerStats {
	    cpuPercent: number;
	    memTotal: number;
	    memUsed: number;
	    hasCPU: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ServerStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cpuPercent = source["cpuPercent"];
	        this.memTotal = source["memTotal"];
	        this.memUsed = source["memUsed"];
	        this.hasCPU = source["hasCPU"];
	    }
	}

}

export namespace updater {
	
	export class UpdateInfo {
	    latestVersion: string;
	    releaseUrl: string;
	    assetUrl: string;
	    releaseNotes: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.latestVersion = source["latestVersion"];
	        this.releaseUrl = source["releaseUrl"];
	        this.assetUrl = source["assetUrl"];
	        this.releaseNotes = source["releaseNotes"];
	    }
	}

}
