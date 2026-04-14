export namespace main {
	
	export class AppSettings {
	    active_protocol: string;
	    kill_switch_enabled: boolean;
	    auto_connect_on_start: boolean;
	    autostart_enabled: boolean;
	    minimize_to_tray: boolean;
	    theme: string;
	    dns_override?: string;
	    routing_mode: string;
	    log_level: string;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_protocol = source["active_protocol"];
	        this.kill_switch_enabled = source["kill_switch_enabled"];
	        this.auto_connect_on_start = source["auto_connect_on_start"];
	        this.autostart_enabled = source["autostart_enabled"];
	        this.minimize_to_tray = source["minimize_to_tray"];
	        this.theme = source["theme"];
	        this.dns_override = source["dns_override"];
	        this.routing_mode = source["routing_mode"];
	        this.log_level = source["log_level"];
	    }
	}
	export class PlatformFeatures {
	    kill_switch_supported: boolean;
	    auto_connect_supported: boolean;
	    autostart_supported: boolean;
	    tray_supported: boolean;
	    platform: string;
	
	    static createFrom(source: any = {}) {
	        return new PlatformFeatures(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kill_switch_supported = source["kill_switch_supported"];
	        this.auto_connect_supported = source["auto_connect_supported"];
	        this.autostart_supported = source["autostart_supported"];
	        this.tray_supported = source["tray_supported"];
	        this.platform = source["platform"];
	    }
	}
	export class ProtocolConfig {
	    protocol: string;
	    config_content: string;
	    filename: string;
	    server_address: string;
	    local_address: string;
	    added_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProtocolConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.protocol = source["protocol"];
	        this.config_content = source["config_content"];
	        this.filename = source["filename"];
	        this.server_address = source["server_address"];
	        this.local_address = source["local_address"];
	        this.added_at = source["added_at"];
	    }
	}
	export class ProtocolInfo {
	    name: string;
	    available: boolean;
	    display_name: string;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new ProtocolInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.available = source["available"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	    }
	}
	export class SavedConnection {
	    id: string;
	    name: string;
	    active_protocol: string;
	    protocols: ProtocolConfig[];
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    last_connected?: any;
	    is_favorite: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SavedConnection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.active_protocol = source["active_protocol"];
	        this.protocols = this.convertValues(source["protocols"], ProtocolConfig);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.last_connected = this.convertValues(source["last_connected"], null);
	        this.is_favorite = source["is_favorite"];
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
	export class StatusResponse {
	    connected: boolean;
	    active_protocol: string;
	    available_protocols: string[];
	    server_address?: string;
	    local_address?: string;
	    bytes_rx: number;
	    bytes_tx: number;
	    connected_at?: string;
	    last_handshake?: string;
	    latency_ms?: number;
	    uptime?: string;
	    kill_switch_enabled: boolean;
	    auto_connect_enabled: boolean;
	    connection_name?: string;
	    connection_id?: string;
	    connection_protocols?: string[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.active_protocol = source["active_protocol"];
	        this.available_protocols = source["available_protocols"];
	        this.server_address = source["server_address"];
	        this.local_address = source["local_address"];
	        this.bytes_rx = source["bytes_rx"];
	        this.bytes_tx = source["bytes_tx"];
	        this.connected_at = source["connected_at"];
	        this.last_handshake = source["last_handshake"];
	        this.latency_ms = source["latency_ms"];
	        this.uptime = source["uptime"];
	        this.kill_switch_enabled = source["kill_switch_enabled"];
	        this.auto_connect_enabled = source["auto_connect_enabled"];
	        this.connection_name = source["connection_name"];
	        this.connection_id = source["connection_id"];
	        this.connection_protocols = source["connection_protocols"];
	        this.error = source["error"];
	    }
	}

}

