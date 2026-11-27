'use strict';
'require view';
'require poll';
'require rpc';
'require form';
'require uci';
'require ui';

/*
 * AC agent status page.
 *
 * All device state comes from the luci.ac_agent rpcd backend, which merges the
 * agent's status file with the process and rc.d state. The view never reads a
 * file or executes a command itself: the ACL then grants named methods instead
 * of "run this binary", and the merge of "process alive" and "file fresh"
 * happens in one place instead of being re-derived in JavaScript.
 *
 * The badge wording lives here, not in the backend, because it has to be
 * translatable: the backend returns a closed set of state names and this file
 * maps them through _().
 */

var POLL_INTERVAL = 10;

/* The wire protocol this interface was built from; shared/proto/proto.go is
 * the single definition. The running agent reports its own value in the status
 * file, so a release mismatch between the packages and the binary is visible
 * here instead of surfacing as unparseable heartbeats. */
var INTERFACE_PROTOCOL = 9;

var callStatus = rpc.declare({ object: 'luci.ac_agent', method: 'status' });
var callRestart = rpc.declare({ object: 'luci.ac_agent', method: 'restart' });
var callApplyEnabled = rpc.declare({ object: 'luci.ac_agent', method: 'applyEnabled' });
var callResetManagement = rpc.declare({
	object: 'luci.ac_agent', method: 'resetManagement'
});

/* The agent's closed state set; see internal/agent/status.go. */
var STATES = {
	managed:     { label: _('Managed'),                       color: '#2e7d32' },
	connected:   { label: _('Connected, awaiting adoption'),  color: '#ef6c00' },
	unreachable: { label: _('Controller unreachable'),        color: '#c62828' },
	discovering: { label: _('No controller found'),           color: '#c62828' },
	starting:    { label: _('Starting'),                      color: '#546e7a' },
	stale:       { label: _('Agent not responding'),          color: '#455a64' },
	stopped:     { label: _('Agent stopped'),                 color: '#455a64' }
};

/* A status file older than three heartbeats means the process stopped writing:
 * either it is gone or it is wedged. Ninety seconds is the floor so a very short
 * interval cannot make the page flicker. */
function isStale(status) {
	if (!status || !status.updated_at)
		return true;

	var written = Date.parse(status.updated_at);
	if (isNaN(written))
		return true;

	var limit = Math.max(3 * (status.interval_seconds || 20), 90) * 1000;
	return (Date.now() - written) > limit;
}

function effectiveState(data) {
	var status = data && data.status;

	if (!data || data.running !== true)
		return 'stopped';
	if (!status)
		return 'starting';
	if (isStale(status))
		return 'stale';
	return STATES[status.state] ? status.state : 'starting';
}

function sourceLabel(source) {
	switch (source) {
	case 'config':     return _('configured address');
	case 'discovered': return _('discovered by broadcast');
	default:           return '-';
	}
}

/* The agent reports "none" when nrsyncd/usteer/dawn are all disabled. Roaming
 * is then handled by the agent's own 11v suggestion loop, so display that
 * instead of the misleading "none". */
function steeringLabel(daemon) {
	return daemon === 'none' ? _('agent') : daemon;
}

/* The controller serves its web UI on the same host:port the agent talks to.
 * A bare "10.10.30.5:8090" from the config gets an http scheme; anything that
 * already carries one is left alone. */
function controllerURL(addr) {
	if (!addr)
		return null;

	var url = String(addr).trim();
	if (!url)
		return null;
	if (!/^(https?:\/\/)/i.test(url))
		url = 'http://' + url;
	return url.replace(/\/+$/, '') + '/';
}

function fmtTime(iso) {
	if (!iso)
		return '-';

	var t = Date.parse(iso);
	return isNaN(t) ? iso : new Date(t).toLocaleString();
}

/* One verdict, same vocabulary as the controller's device list. "rev 5" alone
 * does not answer the question an operator actually has — is this AP running
 * what the controller issued — and a matching rev still says nothing about a
 * local edit. Drift wins over the rev comparison because it is the stronger
 * finding: the files no longer match what was applied. */
function configVerdict(status) {
	if (!status)
		return { label: _('nothing applied yet'), color: '#546e7a' };
	if (status.drift)
		return { label: _('local drift'), color: '#ef6c00' };
	if (status.desired_rev !== status.applied_rev)
		return { label: _('awaiting push'), color: '#3b82f6' };
	if (status.last_error)
		return { label: _('last apply failed'), color: '#c62828' };
	if (!status.applied_rev)
		return { label: _('nothing applied yet'), color: '#546e7a' };
	return { label: _('in sync'), color: '#2e7d32' };
}

function fmtRev(status) {
	if (!status)
		return '-';
	if (!status.applied_rev && !status.desired_rev)
		return _('nothing applied yet');

	var parts = [ _('rev %d applied').format(status.applied_rev) ];
	if (status.desired_rev !== status.applied_rev)
		parts.push(_('rev %d pending').format(status.desired_rev));
	if (status.sha256)
		parts.push('sha ' + status.sha256.slice(0, 8));
	if (status.applied_at)
		parts.push(fmtTime(status.applied_at));
	return parts.join(' · ');
}

/* One sentence explaining the badge and, when it is bad, the most likely cause.
 * A red badge with no reason is worse than no badge. */
function stateHint(state, status) {
	switch (state) {
	case 'managed':
		return status && status.ac_id
			? _('This access point is running a revision issued by controller %s.').format(status.ac_id)
			: _('This access point is running a revision issued by the controller.');
	case 'connected':
		return (status && status.applied_rev > 0)
			? _('The controller no longer knows this access point, but rev %d is still running here. Adopt it again to resume management.').format(status.applied_rev)
			: _('The controller answers but has not adopted this access point yet. Adopt it in the controller interface; until then no configuration is pushed.');
	case 'unreachable':
		return status && status.last_hello_error
			? _('Last error: %s').format(status.last_hello_error)
			: _('The configured address does not answer.');
	case 'discovering':
		return _('No controller address is configured and no UDP answer arrived on the local segment. A controller behind a router never sees the broadcast, so enter its address above.');
	case 'stale':
		return _('The agent has not updated its status for more than three heartbeats. Check the process; a restart usually recovers it.');
	default:
		return _('The agent process is not running, so this access point is not under any controller.');
	}
}

function badge(text, color) {
	return E('span', {
		style: 'display:inline-block;padding:2px 10px;border-radius:10px;' +
			'color:#fff;font-weight:bold;background:' + color
	}, text);
}

function valueRow(title, value) {
	return E('div', { class: 'ac-status-row' }, [
		E('div', { class: 'ac-status-label' }, title),
		E('div', { class: 'ac-status-value' }, value)
	]);
}

/* Scope layout to this view: theme footer and float rules must not position
 * status actions or make long error messages widen the card. */
var PAGE_STYLE =
	'.ac-agent-view{min-width:0;max-width:100%;}' +
	'.ac-agent-view .ac-status-content{padding:16px 20px 20px;min-width:0;}' +
	'.ac-agent-view .ac-status-row{display:grid;grid-template-columns:10rem minmax(0,1fr);gap:8px 16px;padding:10px 0;}' +
	'.ac-agent-view .ac-status-label{opacity:.75;}' +
	'.ac-agent-view .ac-status-value,.ac-agent-view .cbi-value-description{min-width:0;overflow-wrap:anywhere;white-space:normal;}' +
	'.ac-agent-view .ac-status-actions{display:flex;align-items:center;flex-wrap:wrap;gap:12px;margin-top:20px;padding-top:16px;border-top:1px solid rgba(128,128,128,.25);}' +
	'.ac-agent-view .ac-status-actions .cbi-button{float:none!important;position:static!important;margin:0!important;max-width:100%;white-space:normal;}' +
	'.ac-agent-view .ac-status-actions .ac-reset-action{margin-left:auto!important;}' +
	'.ac-agent-view .cbi-value-field{min-width:0;}' +
	'.ac-agent-view input:not([type=checkbox]),.ac-agent-view select{box-sizing:border-box;max-width:100%;}' +
	'@media(max-width:600px){' +
	'.ac-agent-view .ac-status-content{padding:12px 16px 16px;}' +
	'.ac-agent-view .ac-status-row{grid-template-columns:minmax(0,1fr);gap:4px;}' +
	'.ac-agent-view .ac-status-actions .cbi-button{flex:1 1 10rem;}' +
	'.ac-agent-view .ac-status-actions .ac-reset-action{margin-left:0!important;}}';

return view.extend({
	load: function() {
		return callStatus().catch(function() { return {}; }).then(function(data) {
			return { data: data || {} };
		});
	},

	render: function(res) {
		var self = this;

		this.data = res.data || {};
		this.card = E('div', { class: 'cbi-section' });

		/* The heading is rendered above the status card instead of by form.Map:
		 * the page order is title, status, settings. A map title would land
		 * after the card, because the map renders where the form is. */
		var m = new form.Map('ac', '', '');

		var s = m.section(form.NamedSection, 'agent', 'agent', _('Controller connection'));
		s.anonymous = true;
		s.addremove = false;

		var o = s.option(form.Flag, 'enabled', _('Enable'),
			_('Starts the agent and enables it at boot when you save the settings.'));
		o.default = '1';
		o.rmempty = false;

		o = s.option(form.Value, 'ac_addr', _('Controller address'),
			_('host:port of the controller. ' +
			  'Leave empty to find the controller by UDP broadcast on the local segment; ' +
			  'A controller on the other side of a router must be entered explicitly, ' +
			  'because broadcast never crosses layer 3.'));
		o.placeholder = 'controller:8090';
		o.rmempty = true;

		var discovered = this.data.status;

		/* This is a form-only switch. Turning it off removes the legacy secret,
		 * so a hidden saved secret cannot keep selecting legacy authentication. */
		o = s.option(form.Flag, '_legacy_auth', _('Legacy authentication'),
			_('Automatic adoption uses this AP\'s device identity and needs no shared secret. Enable only for an older controller.'));
		o.default = '0';
		o.rmempty = false;
		o.cfgvalue = function(section_id) {
			return discovered && discovered.controller_override ? '0' : (uci.get('ac', section_id, 'secret') ? '1' : '0');
		};
		o.write = function(section_id, value) {
			if (value !== '1') uci.unset('ac', section_id, 'secret');
		};
		if (discovered && discovered.controller_override) o.readonly = true;

		o = s.option(form.Value, 'secret', _('Shared secret'),
			_('Only used by legacy authentication. Turn off legacy authentication to use automatic adoption.'));
		o.depends('_legacy_auth', '1');
		o.password = true;
		if (discovered && discovered.controller_override) {
			o.readonly = true;
			o.cfgvalue = function() { return ''; };
		}
		o.rmempty = true;
		o.validate = function(section_id, value) {
			return !value || value.trim().length >= 12 || _('Use at least 12 characters.');
		};

		o = s.option(form.Value, 'interval', _('Heartbeat interval'),
			_('Seconds between two polls. The default is 20.'));
		o.datatype = 'uinteger';
		o.placeholder = '20';
		o.rmempty = true;

		o = s.option(form.Value, 'peer_port', _('Peer coordination port'),
			_('UDP port used to exchange 802.11k data between access points directly. 0 disables it.'));
		o.datatype = 'uinteger';
		o.placeholder = '29821';
		o.rmempty = true;

		o = s.option(form.Value, 'peer_interval', _('Peer announcement interval'),
			_('How often this access point announces its BSS and client state to its peers, for example 30s.'));
		o.placeholder = '30s';
		o.rmempty = true;

		o = s.option(form.Value, 'peer_network', _('Peer coordination network'),
			_('Logical OpenWrt network, for example lan, that carries AP-to-AP traffic. ' +
			  'The agent resolves its l3_device and joins IPv6 link-local multicast on it; ' +
			  'when IPv6 is unavailable it falls back to IPv4 broadcast.'));
		o.placeholder = 'lan';
		o.rmempty = true;

		/* On by default: the controller still decides while it answers, but when
		 * it is lost the APs keep exchanging BSS/client state directly on UDP
		 * 29821 and can suggest a roam themselves. That works for any number of
		 * access points on the same management segment and does not depend on
		 * nrsyncd or another steering daemon. */
		o = s.option(form.Flag, 'peer_steer', _('Local roam suggestion'),
			_('Let the agent itself send one 802.11v transition suggestion (BTM) when a client is very weak ' +
			  'and a peer serves the same SSID. This is built into the agent, needs no steering daemon, and is ' +
			  'on by default so roaming keeps working across any number of access points after the controller ' +
			  'is lost. It stays silent whenever usteer or dawn is enabled; nrsyncd only distributes 11k ' +
			  'neighbour lists and does not compete.'));
		o.default = '1';
		o.rmempty = false;

		/* Save, then apply: applying the ac config raises procd's reload trigger
		 * for this service, which restarts the agent with the new values. */
		var save = function() {
			return m.save().then(function() {
				return uci.apply();
			}).then(function() {
				/* The switch is applied after the config is committed:
				 * applyEnabled reads /etc/config/ac, so it must see the
				 * value just saved, not the pre-save one. */
				return callApplyEnabled();
			}).then(function(res) {
				if (res && res.ok === false)
					throw new Error((res && res.message) || 'unknown error');
				ui.addNotification(null, E('p', {},
				_('Settings saved. The agent now follows the enable switch.')), 'info');
				return self.refresh();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {},
					_('Could not save the settings: %s').format(err)), 'error');
			});
		};

		/* Exactly one save bar may exist, and who owns it depends on the release:
		 *
		 *   - 24.10 and later: the view base class renders the footer itself
		 *     (luci.js addFooter()) and calls the view's handleSaveApply().
		 *   - 23.05 and earlier: neither the view class nor form.Map renders a
		 *     footer, so the view has to build the bar.
		 *
		 * addFooter() is the discriminator because it is the function that draws
		 * the bar. Deciding on form.Map.handleSave instead is what produced two
		 * bars: no released form.js defines that method (not 23.05, not current),
		 * so the check always reported "no builtin footer" and this view added
		 * its own row on top of the one the framework had already drawn. */
		var frameworkFooter = !!(view && view.prototype &&
			typeof view.prototype.addFooter === 'function');

		if (frameworkFooter)
			self.saveForm = save;

		return m.render().then(function(formNode) {
			/* Title, then the live status card, then the settings form. */
			var wrapper = E('div', { class: 'ac-agent-view' }, [
				E('style', {}, PAGE_STYLE),
				E('h2', { name: 'content' }, _('AC Agent')),
				E('div', { class: 'cbi-map-descr' },
					_('Shows whether this access point is under controller management, and how it reaches the controller.')),
				self.card,
				formNode
			]);

			if (!frameworkFooter)
				wrapper.appendChild(E('div', { class: 'cbi-page-actions' }, [
					E('button', {
						class: 'btn cbi-button cbi-button-save',
						click: function(ev) {
							ev.preventDefault();
							return save();
						}
					}, _('Save & apply')),
					' ',
					E('button', {
						class: 'btn cbi-button',
						click: function(ev) {
							ev.preventDefault();
							return m.reset();
						}
					}, _('Reset'))
				]));

			self.fillCard();
			poll.add(function() {
				return self.refresh();
			}, POLL_INTERVAL);
			return wrapper;
		});
	},

	/* Called by the framework footer of current LuCI. The plain "Save" button is
	 * suppressed (handleSave = null): saving without applying would leave the
	 * agent running the previous revision, which is never what this page means.
	 * Reset keeps the inherited behaviour, which resets every cbi-map here. */
	handleSave: null,

	handleSaveApply: function() {
		return (typeof this.saveForm === 'function') ? this.saveForm() : null;
	},

	refresh: function() {
		var self = this;

		return callStatus().then(function(d) {
			self.data = d || {};
			self.fillCard();
		}, function() {
			/* A failing backend must not stop the poll loop: the next tick
			 * retries and the card keeps its previous contents. */
		});
	},

	fillCard: function() {
		var data = this.data || {};
		var status = data.status;
		var state = effectiveState(data);
		var meta = STATES[state] || STATES.starting;

		while (this.card.firstChild)
			this.card.removeChild(this.card.firstChild);

		this.card.appendChild(E('h3', {}, _('Control status')));
		var content = E('div', { class: 'ac-status-content' });
		this.card.appendChild(content);

		content.appendChild(valueRow(_('Status'), [
			badge(meta.label, meta.color),
			E('div', { style: 'margin-top:6px;max-width:52em' }, stateHint(state, status))
		]));

		if (status) {
			var controller = status.ac_addr
				? [ status.ac_addr, ' ', E('em', {}, '(' + sourceLabel(status.ac_addr_source) + ')') ]
				: [ '-' ];

			content.appendChild(valueRow(_('Controller'), controller));

			content.appendChild(valueRow(_('Controller ID'), status.ac_id || '-'));

			var verdict = configVerdict(status);
			content.appendChild(valueRow(_('Configuration'), [
				badge(verdict.label, verdict.color),
				status.applied_rev || status.desired_rev
					? E('div', { style: 'margin-top:6px' }, fmtRev(status)) : ''
			]));

			/* Drift and the last apply error used to live only in the
			 * controller's view or in a log file. Naming the changed files here
			 * lets an operator decide whether to re-push without SSHing in. */
			if (status.drift)
				content.appendChild(valueRow(_('Configuration drift'),
					(status.drift_files && status.drift_files.length)
						? _('changed on the AP: %s').format(status.drift_files.join(', '))
						: _('files written by the controller no longer match the local copy')));

			if (status.last_error)
				content.appendChild(valueRow(_('Last error'), status.last_error));

			content.appendChild(valueRow(_('Last heartbeat'),
				status.last_hello_at
					? [ fmtTime(status.last_hello_at), ' ', status.last_hello_ok ? _('(accepted)') : _('(failed)') ]
					: '-'));

			content.appendChild(valueRow(_('Access point'),
				[ status.mac || '-', ' ', E('em', {}, status.model || '') ]));

			if (status.steering)
				content.appendChild(valueRow(_('Roaming steering'),
					_('running %s').format(steeringLabel(status.steering))));

			if (status.bss)
				content.appendChild(valueRow(_('BSS reported'), String(status.bss)));

			if (status.peer_network) {
				var link = [ status.peer_network, status.peer_device, status.peer_transport ].filter(Boolean).join(' / ');
				content.appendChild(valueRow(_('Peer coordination'),
					_('%d APs, %d BSSes · %s').format(status.peer_aps || 0, status.peer_bss || 0, link)));
			}

			/* The version an operator can act on: binary version, the protocol it
			 * speaks, and when it started. "agent 0.3.0" alone forced the reader
			 * to guess which controller releases pair with it. */
			content.appendChild(valueRow(_('Agent version'),
				[ 'ac-agent ', E('strong', {}, String(status.version || '-')),
				  status.protocol_version
					  ? ' ' + _('protocol v%d').format(status.protocol_version)
					  : '',
				  ' · ', _('since'), ' ', fmtTime(status.started_at) ]));

			/* The agent and the controller must speak the same protocol; the
			 * controller release that introduced protocol 9 is v0.7.0. An agent
			 * older than protocol reporting gets upgrade advice instead of a
			 * version number it cannot verify. */
			if (status.protocol_version &&
					status.protocol_version !== INTERFACE_PROTOCOL)
				content.appendChild(E('div', { class: 'alert-message warning' },
					_('The running agent speaks protocol v%d but this interface expects v%d. Upgrade luci-app-ac-agent and ac-agent together.')
						.format(status.protocol_version, INTERFACE_PROTOCOL)));

			content.appendChild(valueRow(_('Matching controller'),
				status.protocol_version
					? _('Requires an AC controller of v0.7.0 or newer (protocol version %d). Upgrade the controller and the agent together; a mismatched controller rejects the heartbeat or cannot parse it.')
						.format(status.protocol_version)
					: _('The running agent does not report a protocol version. Upgrade ac-agent and the AC controller together (protocol version 9).')));

			/* Shown whenever the last exchange failed for a reason the badge does
			 * not already spell out (for instance a changed controller key). */
			if (status.last_hello_error && state !== 'unreachable')
				content.appendChild(E('div', { class: 'alert-message warning' },
					_('Last error: %s').format(status.last_hello_error)));
		}

		var actions = E('div', { class: 'ac-status-actions' });
		var url = controllerURL(status && status.ac_addr);
		if (url)
			actions.appendChild(E('a', {
				class: 'btn cbi-button cbi-button-action', href: url,
				target: '_blank', rel: 'noopener noreferrer',
				title: _('Open the controller web interface in a new tab')
			}, _('Open controller')));
		[
			E('button', {
				type: 'button',
				class: 'btn cbi-button cbi-button-action',
				click: ui.createHandlerFn(this, 'handleRestart')
			}, _('Restart agent')),
			E('button', {
				type: 'button',
				class: 'btn cbi-button cbi-button-negative ac-reset-action',
				style: 'background:#c62828;border-color:#c62828;color:#fff',
				click: ui.createHandlerFn(this, 'handleResetManagement')
			}, _('Reset management'))
		].forEach(function(button) { actions.appendChild(button); });
		content.appendChild(actions);
	},

	/* Restarting runs one sync cycle immediately, which is what an operator wants
	 * after editing the controller address. There is deliberately no separate
	 * "sync now": a one-shot agent would race the resident process. */
	handleRestart: function() {
		var self = this;

		return callRestart().then(function(res) {
			ui.addNotification(null, E('p', {}, (res && res.ok)
				? _('Agent restarted.')
				: _('Could not restart the agent: %s').format((res && res.message) || 'unknown error')),
				(res && res.ok) ? 'info' : 'error');
			return self.refresh();
		}, function(err) {
			ui.addNotification(null, E('p', {},
				_('Could not restart the agent: %s').format(err)), 'error');
		});
	},

	handleResetManagement: function() {
		/* The confirmation carries the same red as the trigger: a destructive
		 * action must read as one before and after the click. */
		var submit = E('button', {
			class: 'btn cbi-button cbi-button-negative',
			style: 'background:#c62828;border-color:#c62828;color:#fff',
			type: 'button'
		}, _('Reset management'));
		var cancel = E('button', { class: 'btn cbi-button', type: 'button', click: ui.hideModal }, _('Cancel'));
		submit.addEventListener('click', function() {
			submit.disabled = cancel.disabled = true;
			callResetManagement().then(function(res) {
				if (!res || !res.ok)
					throw new Error((res && res.message) || 'unknown error');
				window.location.reload();
			}).catch(function(err) {
				submit.disabled = cancel.disabled = false;
				ui.addNotification(null, E('p', {}, _('Could not reset management: %s').format(err)), 'error');
			});
		});
		ui.showModal(_('Reset management'), [
			E('p', {}, _('This removes the controller binding and keeps current Wi-Fi and network settings. Adopt this AP on the controller you want to use.')),
			E('div', { class: 'right' }, [ cancel, ' ', submit ])
		]);
	}
});
