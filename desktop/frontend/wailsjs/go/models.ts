export namespace main {
	
	export class ConnectOnDemandSettings {
	    enabled: boolean;
	    trigger: string;
	    ssid_mode: string;
	    ssid_list: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConnectOnDemandSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.trigger = source["trigger"];
	        this.ssid_mode = source["ssid_mode"];
	        this.ssid_list = source["ssid_list"];
	    }
	}
	export class AppSettings {
	    active_protocol: string;
	    kill_switch_enabled: boolean;
	    auto_connect_on_start: boolean;
	    connect_on_demand: ConnectOnDemandSettings;
	    autostart_enabled: boolean;
	    minimize_to_tray: boolean;
	    theme: string;
	    app_language?: string;
	    dns_override?: string;
	    log_level: string;
	    gateway_url?: string;
	    api_key?: string;
	    tunnel_health_mode?: string;
	    tunnel_health_target?: string;
	    tunnel_health_ping_interval_sec?: number;
	    tunnel_health_dead_threshold?: number;
	    network_rules_enabled?: boolean;
	    reconnect_on_system_wake?: boolean;
	    prevent_display_sleep?: boolean;
	    protocol_failover_order?: string[];
	    encrypted_at_rest?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_protocol = source["active_protocol"];
	        this.kill_switch_enabled = source["kill_switch_enabled"];
	        this.auto_connect_on_start = source["auto_connect_on_start"];
	        this.connect_on_demand = this.convertValues(source["connect_on_demand"], ConnectOnDemandSettings);
	        this.autostart_enabled = source["autostart_enabled"];
	        this.minimize_to_tray = source["minimize_to_tray"];
	        this.theme = source["theme"];
	        this.app_language = source["app_language"];
	        this.dns_override = source["dns_override"];
	        this.log_level = source["log_level"];
	        this.gateway_url = source["gateway_url"];
	        this.api_key = source["api_key"];
	        this.tunnel_health_mode = source["tunnel_health_mode"];
	        this.tunnel_health_target = source["tunnel_health_target"];
	        this.tunnel_health_ping_interval_sec = source["tunnel_health_ping_interval_sec"];
	        this.tunnel_health_dead_threshold = source["tunnel_health_dead_threshold"];
	        this.network_rules_enabled = source["network_rules_enabled"];
	        this.reconnect_on_system_wake = source["reconnect_on_system_wake"];
	        this.prevent_display_sleep = source["prevent_display_sleep"];
	        this.protocol_failover_order = source["protocol_failover_order"];
	        this.encrypted_at_rest = source["encrypted_at_rest"];
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
	export class PoolListItem {
	    id: string;
	    name: string;
	    policy: string;
	    member_count: number;
	    active_member_id?: string;
	    active_member_name?: string;
	    active_member_cc?: string;
	    pending_member_id?: string;
	    pending_member_name?: string;
	    pending_member_cc?: string;
	    is_active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PoolListItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.policy = source["policy"];
	        this.member_count = source["member_count"];
	        this.active_member_id = source["active_member_id"];
	        this.active_member_name = source["active_member_name"];
	        this.active_member_cc = source["active_member_cc"];
	        this.pending_member_id = source["pending_member_id"];
	        this.pending_member_name = source["pending_member_name"];
	        this.pending_member_cc = source["pending_member_cc"];
	        this.is_active = source["is_active"];
	    }
	}
	export class BootstrapStateInfo {
	    active_pool_id: string;
	    active_pool?: PoolListItem;
	    has_active_single: boolean;
	    active_single_name?: string;
	
	    static createFrom(source: any = {}) {
	        return new BootstrapStateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_pool_id = source["active_pool_id"];
	        this.active_pool = this.convertValues(source["active_pool"], PoolListItem);
	        this.has_active_single = source["has_active_single"];
	        this.active_single_name = source["active_single_name"];
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
	
	export class ConnectionConfigDescriptor {
	    id: string;
	    protocol: string;
	    nickname?: string;
	    filename?: string;
	    server_address?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionConfigDescriptor(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.protocol = source["protocol"];
	        this.nickname = source["nickname"];
	        this.filename = source["filename"];
	        this.server_address = source["server_address"];
	    }
	}
	export class DnsProvider {
	    id: string;
	    label: string;
	    servers: string[];
	    dot_host?: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new DnsProvider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.servers = source["servers"];
	        this.dot_host = source["dot_host"];
	        this.note = source["note"];
	    }
	}
	export class DnsTestResult {
	    host: string;
	    addresses: string[];
	    duration_ms: number;
	    resolver_hint: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new DnsTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.addresses = source["addresses"];
	        this.duration_ms = source["duration_ms"];
	        this.resolver_hint = source["resolver_hint"];
	        this.error = source["error"];
	    }
	}
	export class EntitlementState {
	    source: string;
	    license_key?: string;
	    is_pro: boolean;
	    // Go type: time
	    first_activated?: any;
	    // Go type: time
	    last_verified?: any;
	
	    static createFrom(source: any = {}) {
	        return new EntitlementState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.license_key = source["license_key"];
	        this.is_pro = source["is_pro"];
	        this.first_activated = this.convertValues(source["first_activated"], null);
	        this.last_verified = this.convertValues(source["last_verified"], null);
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
	export class HelperStatus {
	    installed: boolean;
	    running: boolean;
	    platform: string;
	
	    static createFrom(source: any = {}) {
	        return new HelperStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.platform = source["platform"];
	    }
	}
	export class NetworkRule {
	    id: string;
	    priority: number;
	    match_type: string;
	    match_value: string;
	    action: string;
	    target_id?: string;
	    enabled: boolean;
	    name?: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkRule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.priority = source["priority"];
	        this.match_type = source["match_type"];
	        this.match_value = source["match_value"];
	        this.action = source["action"];
	        this.target_id = source["target_id"];
	        this.enabled = source["enabled"];
	        this.name = source["name"];
	    }
	}
	export class NetworkRulesEvalSnapshot {
	    network_type: string;
	    ssid: string;
	    master_enabled: boolean;
	    has_rules: boolean;
	    engine_active: boolean;
	    rule_matching: boolean;
	
	    static createFrom(source: any = {}) {
	        return new NetworkRulesEvalSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.network_type = source["network_type"];
	        this.ssid = source["ssid"];
	        this.master_enabled = source["master_enabled"];
	        this.has_rules = source["has_rules"];
	        this.engine_active = source["engine_active"];
	        this.rule_matching = source["rule_matching"];
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
	export class PoolSplitTunnel {
	    bypass_cidrs?: string[];
	    exclude_private_networks?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PoolSplitTunnel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bypass_cidrs = source["bypass_cidrs"];
	        this.exclude_private_networks = source["exclude_private_networks"];
	    }
	}
	export class ProtocolConfig {
	    id?: string;
	    protocol: string;
	    config_content: string;
	    filename: string;
	    nickname?: string;
	    server_address: string;
	    local_address: string;
	    added_at: string;
	    windows_routes_script?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProtocolConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.protocol = source["protocol"];
	        this.config_content = source["config_content"];
	        this.filename = source["filename"];
	        this.nickname = source["nickname"];
	        this.server_address = source["server_address"];
	        this.local_address = source["local_address"];
	        this.added_at = source["added_at"];
	        this.windows_routes_script = source["windows_routes_script"];
	    }
	}
	export class PoolMember {
	    id: string;
	    name: string;
	    config?: ProtocolConfig;
	    country: string;
	    region: string;
	    active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PoolMember(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.config = this.convertValues(source["config"], ProtocolConfig);
	        this.country = source["country"];
	        this.region = source["region"];
	        this.active = source["active"];
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
	export class PoolRotation {
	    interval_min: number;
	    idle_aware: boolean;
	    force_after_min: number;
	
	    static createFrom(source: any = {}) {
	        return new PoolRotation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.interval_min = source["interval_min"];
	        this.idle_aware = source["idle_aware"];
	        this.force_after_min = source["force_after_min"];
	    }
	}
	export class Pool {
	    id: string;
	    name: string;
	    // Go type: time
	    created_at: any;
	    policy: string;
	    rotation: PoolRotation;
	    members: PoolMember[];
	    country_override?: string;
	    restrict_regions?: string[];
	    split_tunnel?: PoolSplitTunnel;
	    dns_override?: string;
	
	    static createFrom(source: any = {}) {
	        return new Pool(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.policy = source["policy"];
	        this.rotation = this.convertValues(source["rotation"], PoolRotation);
	        this.members = this.convertValues(source["members"], PoolMember);
	        this.country_override = source["country_override"];
	        this.restrict_regions = source["restrict_regions"];
	        this.split_tunnel = this.convertValues(source["split_tunnel"], PoolSplitTunnel);
	        this.dns_override = source["dns_override"];
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
	export class RegionCoverage {
	    region: string;
	    servers: number;
	    countries: number;
	
	    static createFrom(source: any = {}) {
	        return new RegionCoverage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.region = source["region"];
	        this.servers = source["servers"];
	        this.countries = source["countries"];
	    }
	}
	export class PoolMemberWire {
	    id: string;
	    name: string;
	    config?: ProtocolConfig;
	    country: string;
	    region: string;
	    active: boolean;
	    unreachable: boolean;
	    last_error?: string;
	    // Go type: time
	    last_unreachable?: any;
	
	    static createFrom(source: any = {}) {
	        return new PoolMemberWire(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.config = this.convertValues(source["config"], ProtocolConfig);
	        this.country = source["country"];
	        this.region = source["region"];
	        this.active = source["active"];
	        this.unreachable = source["unreachable"];
	        this.last_error = source["last_error"];
	        this.last_unreachable = this.convertValues(source["last_unreachable"], null);
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
	export class PoolWire {
	    id: string;
	    name: string;
	    policy: string;
	    members: PoolMemberWire[];
	    rotation?: PoolRotation;
	    restrict_regions?: string[];
	    country_override?: string;
	    split_tunnel?: PoolSplitTunnel;
	    dns_override?: string;
	
	    static createFrom(source: any = {}) {
	        return new PoolWire(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.policy = source["policy"];
	        this.members = this.convertValues(source["members"], PoolMemberWire);
	        this.rotation = this.convertValues(source["rotation"], PoolRotation);
	        this.restrict_regions = source["restrict_regions"];
	        this.country_override = source["country_override"];
	        this.split_tunnel = this.convertValues(source["split_tunnel"], PoolSplitTunnel);
	        this.dns_override = source["dns_override"];
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
	export class PoolDetail {
	    pool?: PoolWire;
	    coverage: RegionCoverage[];
	
	    static createFrom(source: any = {}) {
	        return new PoolDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pool = this.convertValues(source["pool"], PoolWire);
	        this.coverage = this.convertValues(source["coverage"], RegionCoverage);
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
	
	
	
	
	
	export class PoolUpload {
	    filename: string;
	    content: number[];
	
	    static createFrom(source: any = {}) {
	        return new PoolUpload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.content = source["content"];
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
	
	export class RemoteConfigEntry {
	    id: number;
	    peer_name: string;
	    protocol: string;
	    interface_name: string;
	    agent_id: string;
	    vpn_ip: string;
	    status: string;
	    last_handshake?: string;
	    obfuscation_enabled?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RemoteConfigEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.peer_name = source["peer_name"];
	        this.protocol = source["protocol"];
	        this.interface_name = source["interface_name"];
	        this.agent_id = source["agent_id"];
	        this.vpn_ip = source["vpn_ip"];
	        this.status = source["status"];
	        this.last_handshake = source["last_handshake"];
	        this.obfuscation_enabled = source["obfuscation_enabled"];
	    }
	}
	export class RemoteProfile {
	    user: string;
	    count: number;
	    configs: RemoteConfigEntry[];
	
	    static createFrom(source: any = {}) {
	        return new RemoteProfile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user = source["user"];
	        this.count = source["count"];
	        this.configs = this.convertValues(source["configs"], RemoteConfigEntry);
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
	export class RotatorStatus {
	    active: boolean;
	    pool_id?: string;
	    interval_min: number;
	    idle_aware: boolean;
	    next_rotation_in: number;
	    idle_blocked: boolean;
	    force_rotate_in?: number;
	
	    static createFrom(source: any = {}) {
	        return new RotatorStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.pool_id = source["pool_id"];
	        this.interval_min = source["interval_min"];
	        this.idle_aware = source["idle_aware"];
	        this.next_rotation_in = source["next_rotation_in"];
	        this.idle_blocked = source["idle_blocked"];
	        this.force_rotate_in = source["force_rotate_in"];
	    }
	}
	export class SavedConnection {
	    id: string;
	    name: string;
	    active_protocol: string;
	    active_config_id?: string;
	    protocols: ProtocolConfig[];
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    last_connected?: any;
	    is_favorite: boolean;
	    dns_override?: string;
	
	    static createFrom(source: any = {}) {
	        return new SavedConnection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.active_protocol = source["active_protocol"];
	        this.active_config_id = source["active_config_id"];
	        this.protocols = this.convertValues(source["protocols"], ProtocolConfig);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.last_connected = this.convertValues(source["last_connected"], null);
	        this.is_favorite = source["is_favorite"];
	        this.dns_override = source["dns_override"];
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
	    server_country_code?: string;
	    server_city?: string;
	    local_address?: string;
	    bytes_rx: number;
	    bytes_tx: number;
	    connected_at?: string;
	    last_handshake?: string;
	    latency_ms?: number;
	    uptime?: string;
	    kill_switch_enabled: boolean;
	    kill_switch_state: string;
	    auto_connect_enabled: boolean;
	    pause_remaining_sec: number;
	    connection_name?: string;
	    connection_id?: string;
	    connection_protocols?: string[];
	    connection_configs?: ConnectionConfigDescriptor[];
	    connection_active_config_id?: string;
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
	        this.server_country_code = source["server_country_code"];
	        this.server_city = source["server_city"];
	        this.local_address = source["local_address"];
	        this.bytes_rx = source["bytes_rx"];
	        this.bytes_tx = source["bytes_tx"];
	        this.connected_at = source["connected_at"];
	        this.last_handshake = source["last_handshake"];
	        this.latency_ms = source["latency_ms"];
	        this.uptime = source["uptime"];
	        this.kill_switch_enabled = source["kill_switch_enabled"];
	        this.kill_switch_state = source["kill_switch_state"];
	        this.auto_connect_enabled = source["auto_connect_enabled"];
	        this.pause_remaining_sec = source["pause_remaining_sec"];
	        this.connection_name = source["connection_name"];
	        this.connection_id = source["connection_id"];
	        this.connection_protocols = source["connection_protocols"];
	        this.connection_configs = this.convertValues(source["connection_configs"], ConnectionConfigDescriptor);
	        this.connection_active_config_id = source["connection_active_config_id"];
	        this.error = source["error"];
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
	export class UpdatePoolRequest {
	    name: string;
	    policy: string;
	    rotation?: PoolRotation;
	    restrict_regions?: string[];
	    country_override?: string;
	    split_tunnel?: PoolSplitTunnel;
	    dns_override?: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdatePoolRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.policy = source["policy"];
	        this.rotation = this.convertValues(source["rotation"], PoolRotation);
	        this.restrict_regions = source["restrict_regions"];
	        this.country_override = source["country_override"];
	        this.split_tunnel = this.convertValues(source["split_tunnel"], PoolSplitTunnel);
	        this.dns_override = source["dns_override"];
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

