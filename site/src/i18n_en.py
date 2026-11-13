# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
# English rendering of template.html: ordered (French, English) replacements.
# build.py fails if a French string is no longer found, so a change in
# template.html that is not mirrored here stops the build.
PAIRS = [('aria-label="TakeCare Agents, accueil"', 'aria-label="TakeCare Agents, home"'),
 ('aria-label="Navigation principale"', 'aria-label="Main navigation"'),
 ('data-r="deck">Le deck</a>', 'data-r="deck">The deck</a>'),
 ('data-r="regles">Les règles</a>', 'data-r="regles">The rules</a>'),
 ('data-r="lexique">Lexique</a>', 'data-r="lexique">Lexicon</a>'),
 ('data-r="reference">Référence</a>', 'data-r="reference">Reference</a>'),
 ('data-r="contribuer">Contribuer</a>', 'data-r="contribuer">Contribute</a>'),
 ('· opérateur Kubernetes open source<br>Maintenu par Agaetis', '· open source Kubernetes operator<br>Maintained by Agaetis'),
 ('<div>Toujours là, jamais à votre place.<br>', '<div>Always there, never in your place.<br>'),
 ('const nick = r => r.nick.replace(/^(Le |La |Les |L\')/,m=>m.toLowerCase());\nconst bare = r => r.nick.replace(/^(Les |Le |La |L\')/,"");',
  'const nick = r => r.nick.replace(/^The /,"the ");\nconst bare = r => r.nick.replace(/^The /,"");'),
 ('const TY = {agent:["Agent","--c-agent"],guard:["Garde-fou","--c-guard"],exo:["Exercice","--c-exo"],mat:["Matériel","--c-mat"],lieu:["Lieu","--c-lieu"]};',
  'const TY = {agent:["Agent","--c-agent"],guard:["Guardrail","--c-guard"],exo:["Exercise","--c-exo"],mat:["Equipment","--c-mat"],lieu:["Place","--c-lieu"]};'),
 ('const NEXT = {soon:"Bientôt", later:"Plus tard"};', 'const NEXT = {soon:"Next", later:"Later"};'),
 ('alt="Carte : ${esc(r.nick)}" loading="lazy">${st(k)}', 'alt="Card: ${esc(r.nick)}" loading="lazy">${st(k)}'),
 (' doc:"TakeCareDoc, avec un enregistrement TakeCareInvestigation par alerte. Sur une alerte Alertmanager, l\'opérateur ouvre l\'enquête et la confie à l\'agent kagent par A2A. Lecture seule ; le '
  'diagnostic sourcé part sur Slack avec sa trace et son coût. Fonctionne de bout en bout, sur la démo comme sur votre cluster.",',
  ' doc:"TakeCareDoc, with one TakeCareInvestigation record per alert. On an Alertmanager alert, the operator opens the investigation and hands it to the kagent agent over A2A. Read-only; the '
  'sourced diagnosis goes to Slack with its trace and its cost. Works end to end, on the demo as on your cluster.",'),
 (' ranger:"TakeCareRanger : les politiques d\'agentgateway rendues par niveau sur la passerelle par laquelle passent tous les appels (modèles, outils MCP, A2A) : journal, masquage et filtrage (audit à l\'Apprenti, appliqués dès le Compagnon), plafonds de jetons et de requêtes, modèles autorisés par rôle, et à l\'Expert les Balises du client. Pas de boucle LLM : un garde-fou déterministe.",',
  ' ranger:"TakeCareRanger: agentgateway policies rendered by level on the gateway every call goes through (models, MCP tools, A2A): journal, masking and filtering (audited at Apprentice, applied from Practitioner), token and request caps, the models each role may call, and at Expert the customer\'s own Markers. No LLM loop: a deterministic guardrail.",'),
 (' psy:"TakeCarePsy : évalue les traces des agents avec un modèle juge, jamais celui qu\'il juge (une règle CEL le refuse). Il évalue, il n\'agit pas.",',
  ' psy:"TakeCarePsy: evaluates the agents\' traces with a judge model, never the one it judges (a CEL rule refuses it). It evaluates, it does not act.",'),
 (' intendant:"TakeCareIntendant : il relève le terrain par les connecteurs de cluster, recense les agents et les connecteurs en circulation, et propose, dans le statut d\'un TakeCareSurvey, les entrées de la Carte. Il propose chaque entrée ; vos équipes en font une pull request dans leur Git. Livrés : l\'import, la passe agent, le résumé des propositions et l\'inventaire ; le niveau Maître est prévu. Spec 10.",',
  ' intendant:"TakeCareIntendant: it surveys the ground through the cluster connectors, lists the agents and connectors in circulation, and proposes, in a TakeCareSurvey\'s status, the entries of the Map. It proposes each entry; your teams turn it into a pull request in their Git. Delivered: the import, the agent pass, the digest of the proposals and the inventory; the Expert level is planned. Spec 10.",'),
 (' kine:"TakeCareKine, enregistrements TakeCareReview : part des enquêtes du Doc pour remonter aux causes récurrentes et suivre les actions correctives.",',
  ' kine:"TakeCareKine, TakeCareReview records: starts from the Doc\'s investigations to trace recurring causes and follow up corrective actions.",'),
 (' mecano:"TakeCareMecano, enregistrements TakeCareMaintenance : état du parc, plan de montée de version (une PR au niveau Practitioner).",',
  ' mecano:"TakeCareMecano, TakeCareMaintenance records: fleet status, upgrade plan (a PR at the Practitioner level).",'),
 (' coach:"TakeCareCoach, enregistrements TakeCarePeakReview : rejoue vos derniers pics, prépare capacité et quotas.",',
  ' coach:"TakeCareCoach, TakeCarePeakReview records: replays your last peaks, prepares capacity and quotas.",'),
 (' nutri:"TakeCareNutri, enregistrements TakeCareCostReview : sources de coût et usage des modèles mesuré par la passerelle.",',
  ' nutri:"TakeCareNutri, TakeCareCostReview records: cost sources and model usage measured by the gateway.",'),
 (' guide:"TakeCareGuide, enregistrements TakeCareConversation : répond depuis vos runbooks, pas à pas.",',
  ' guide:"TakeCareGuide, TakeCareConversation records: answers from your runbooks, step by step.",'),
 (' campdebase:"TakeCarePlatform, le cœur : modèles, passerelle, moteur et télémétrie, une seule instance par cluster, rendue par le chart. Un cluster d\'administration supervise plusieurs clusters de charge, chacun avec son paquet takecare-cluster et son front mTLS (ADR 0016). Ce qui équipe les agents (Sacs, Carte, compétences, consignes) vit dans le Git du client ; une liste approuvée en fixe chaque version, et l\'opérateur n\'équipe un agent que de ce qui y est épinglé (ADR 0019). Aucun registre d\'agents n\'est installé. La liste approuvée est prévue.",',
  ' campdebase:"TakeCarePlatform, the core: models, gateway, engine and telemetry, a single instance per cluster, rendered by the chart. An administration cluster supervises several workload clusters, each with its takecare-cluster package and its mTLS front (ADR 0016). What equips the agents (Bags, Map, skills, prompts) lives in the customer\'s Git; an approved list fixes each version, and the operator equips an agent only with what is pinned there (ADR 0019). No agent registry is installed. The approved list is planned.",'),
 (' terrain:"Le décor de rejeu. Le mode démo en fournit un premier : un cluster k3d, le décor billing-api et l\'incident scénarisé proxy-timeout. Rejouer votre propre historique demandera le connecteur d\'historique.",',
  ' terrain:"The replay scenery. Demo mode provides a first one: a k3d cluster, the billing-api scenery and the scripted proxy-timeout incident. Replaying your own history will need the history connector.",'),
 (' sac:"La spec du rôle : son niveau, son périmètre, ses connecteurs par type, où partent ses résultats, son modèle. Pour le Doc, c\'est la ressource TakeCareDoc.",',
  ' sac:"The role\'s spec: its level, its scope, its connectors by type, where its results go, its model. For the Doc, that is the TakeCareDoc resource.",'),
 (' dossard:"Aujourd\'hui, l\'identité d\'un agent à la passerelle, c\'est sa route : chaque rôle joint chaque connecteur par une route à lui, dont la politique n\'autorise que ses outils. Le Dossard, remis par l\'Intendant et vérifié par jeton, viendra ensuite.",',
  ' dossard:"Today, an agent\'s identity at the gateway is its route: each role reaches each connector through a route of its own, whose policy allows only its tools. The Bib, handed by the Intendant and checked by token, comes later.",'),
 (' balises:"Les politiques d\'agentgateway : chaque outil autorisé un par un selon le niveau, la clé du fournisseur de modèle jamais exposée, le coût de chaque appel mesuré. Les mêmes pour tous les agents. Le Ranger les rend par niveau.",',
  ' balises:"agentgateway policies: each tool allowed one by one according to the level, the model provider\'s key never exposed, the cost of every call measured. The same for every agent. The Ranger renders them by level.",'),
 (' journal:"Une trace OpenTelemetry par enquête, selon les conventions GenAI, exportée en OTLP (Jaeger dans la démo). En production, le contenu des messages n\'est pas capturé par défaut.",',
  ' journal:"One OpenTelemetry trace per investigation, following the GenAI conventions, exported over OTLP (Jaeger in the demo). In production, message content is not captured by default.",'),
 (' carte:"La cartographie de l\'infra que partagent les agents : ce qui tourne, qui en répond, ce qui en dépend. Des fichiers YAML dans le Git du client, servis par un serveur MCP à cinq outils en lecture (entrée, voisins, rayon d\'impact, chemin, la Carte à une date) ; le Doc la lit pour couvrir son périmètre et l\'opérateur y résout l\'entité d\'une alerte. Tenue par l\'Intendant.",',
  ' carte:"The infra mapping the agents share: what runs, who answers for it, what depends on it. YAML files in the customer\'s Git, served by an MCP server with five read tools (entry, neighbours, blast radius, path, the Map at a date); the Doc reads it to cover its scope and the operator resolves an alert\'s entity in it. Kept by the Intendant.",'),
 (' trousse:"Les actions qu\'un agent peut faire seul au niveau Expert, autorisées une par une.",',
  ' trousse:"The actions an agent may take on its own at the Expert level, allowed one by one.",'),
 (' ration:"L\'enveloppe de coût validée, que le Nutri ne dépasse jamais.",', ' ration:"The validated cost envelope, which the Nutri never exceeds.",'),
 (' plan:"Le plan de capacité d\'un temps fort : seuils, marges et corrections prêtes.",', ' plan:"The capacity plan for a big moment: thresholds, headroom and fixes ready to go.",'),
 (' topo:"Vos runbooks et votre doc, branchés comme connecteur de type knowledge.",', ' topo:"Your runbooks and docs, plugged in as a knowledge-type connector.",'),
 (' reeduc:"Les actions correctives issues des post-mortems, suivies jusqu\'au bout.",', ' reeduc:"The corrective actions from the post-mortems, followed through to the end.",'),
 (' carnet:"Versions, fins de support et mises à jour planifiées, composant par composant.",', ' carnet:"Versions, end of support and planned upgrades, component by component.",'),
 (' bootcamp:"Un exercice, pas une ressource : du décor et des scénarios de panne, comme le mode démo, avec le Doc, le Kiné et le Mécano.",',
  ' bootcamp:"An exercise, not a resource: scenery and failure scenarios, like demo mode, with the Doc, the Kiné and the Mécano.",'),
 (' stresstest:"Un exercice, pas une ressource : montée en charge sur une copie, avec le Coach et le Nutri.",',
  ' stresstest:"An exercise, not a resource: load ramp-up on a copy, with the Coach and the Nutri.",'),
 (' relais:"Un exercice, pas une ressource : transmission du savoir avec le Guide et le Mécano."', ' relais:"An exercise, not a resource: knowledge transfer with the Guide and the Mécano."'),
 (' ["Apprenti, Compagnon, Maître","Les niveaux","spec.level: Apprentice | Practitioner | Expert. Le niveau décide des outils autorisés. Le changer, c\'est modifier la ressource : une PR relue et '
  'validée par vos équipes, un changement tracé."]',
  ' ["Apprentice, Practitioner, Expert","The levels","spec.level: Apprentice | Practitioner | Expert. The level decides which tools are allowed. Changing it means changing the resource: a PR '
  'reviewed and approved by your teams, a traced change."]'),
 (' ["Une enquête","Hors deck","TakeCareInvestigation : un enregistrement par alerte, avec sa phase, son usage et son traceID."],',
  ' ["An investigation","Off deck","TakeCareInvestigation: one record per alert, with its phase, its usage and its traceID."],'),
 (' ["Les outils","Hors deck","TakeCareConnector : des serveurs MCP derrière la passerelle (kubernetes, prometheus, runbooks, Slack…)."],',
  ' ["The tools","Off deck","TakeCareConnector: MCP servers behind the gateway (kubernetes, prometheus, runbooks, Slack…)."],'),
 (' ["Le staff","Hors deck","kubectl get takecare -A : tous les rôles, avec leur niveau et leur état."]',
  ' ["The staff","Off deck","kubectl get takecare -A: every role, with its level and its state."]'),
 ('href="#deck">Ouvrir le deck</a><a class="btn${big?"":" sm"}" href="#demarrer">Lancer la démo</a>', 'href="#deck">Open the deck</a><a class="btn${big?"":" sm"}" href="#demarrer">Run the demo</a>'),
 ('<div class="h1txt"><p class="eyebrow">Opérateur Kubernetes · open source</p><h1>Des agents SRE <em>à l\'épreuve du terrain.</em></h1></div>',
  '<div class="h1txt"><p class="eyebrow">Kubernetes operator · open source</p><h1>SRE agents, <em>proven in the field.</em></h1></div>'),
 ('aria-label="Vidéo TakeCare Agents, 1 minute, sans voix, avec musique"', 'aria-label="TakeCare Agents video, 1 minute, no voice-over, with music (French captions)"'),
 ('aria-label="Le dépôt GitHub"><span class="vd">$</span> git clone github.com/Agaetis-IT/takecare-agents<svg class="vai" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 11 11 5M6 5h5v5" '
  'fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></a><div class="acts"><a class="btn pri" href="#deck">Ouvrir le deck</a><a class="btn '
  'vb" href="#demarrer">Lancer la démo</a></div>',
  'aria-label="The GitHub repository"><span class="vd">$</span> git clone github.com/Agaetis-IT/takecare-agents<svg class="vai" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 11 11 5M6 5h5v5" '
  'fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></a><div class="acts"><a class="btn pri" href="#deck">Open the deck</a><a class="btn vb" '
  'href="#demarrer">Run the demo</a></div>'),
 ('↺ Revoir la vidéo', '↺ Watch again'),
 ('<button class="btn sm skip" id="skip" type="button">Passer la vidéo ›</button><span class="uend"><a class="btn pri sm" href="#deck">Ouvrir le deck</a><a class="btn sm" href="#demarrer">Lancer la '
  'démo</a></span>',
  '<button class="btn sm skip" id="skip" type="button">Skip the video ›</button><span class="uend"><a class="btn pri sm" href="#deck">Open the deck</a><a class="btn sm" href="#demarrer">Run the '
  'demo</a></span>'),
 ('function regles(){ const h = oldHome(); return pick(h,"Les règles · la progression") + pick(h,"Les règles · l\'encadrement"); }',
  'function regles(){ const h = oldHome(); return pick(h,"The rules · progression") + pick(h,"The rules · governance"); }'),
 ('alt="Carte : ${esc(byK[k].nick)}"></a>`}).join("");', 'alt="Card: ${esc(byK[k].nick)}"></a>`}).join("");'),
 ('      <p class="eyebrow">Opérateur Kubernetes · open source</p>\n'
  "      <h1>Des agents SRE <em>en période d'essai.</em></h1>\n"
  '      <p class="lead">TakeCare Agents fait tourner un staff d\'exploitation en agents IA dans votre cluster : neuf rôles, chacun son métier. Chaque agent commence Apprenti, en lecture seule, et '
  'gagne son autonomie palier par palier. Et tous vos agents passent par les mêmes règles, ceux des éditeurs compris.</p>\n'
  '      <div class="acts"><a class="btn pri" href="${REPO}" target="_blank" rel="noopener">Voir le dépôt GitHub ↗</a><a class="btn" href="#deck">Ouvrir le deck</a></div>',
  '      <p class="eyebrow">Kubernetes operator · open source</p>\n'
  '      <h1>SRE agents <em>on probation.</em></h1>\n'
  '      <p class="lead">TakeCare Agents runs an operations staff of AI agents in your cluster: nine roles, each with its own job. Every agent starts as an Apprentice, read-only, and earns its '
  "autonomy level by level. And all your agents go through the same rules, your vendors' included.</p>\n"
  '      <div class="acts"><a class="btn pri" href="${REPO}" target="_blank" rel="noopener">See the GitHub repository ↗</a><a class="btn" href="#deck">Open the deck</a></div>'),
 ('<span class="badge"><span>version</span><b class="m">v0.3.0-rc.1 · le Doc, le Ranger et l\'Intendant</b></span>',
  '<span class="badge"><span>version</span><b class="m">v0.3.0-rc.1 · the Doc, the Ranger and the Intendant</b></span>'),
 ('<span class="badge"><span>moteur</span>', '<span class="badge"><span>engine</span>'),
 ('<span class="badge"><span>passerelle</span>', '<span class="badge"><span>gateway</span>'),
 ('<span class="badge"><span>licence</span>', '<span class="badge"><span>license</span>'),
 ('<span class="c"># k3d, TakeCare, le décor, le Doc</span>', '<span class="c"># k3d, TakeCare, the scenery, the Doc</span>'),
 ('<span class="c"># la panne ; l\'alerte ~2 min 30 après</span>', '<span class="c"># the failure; the alert ~2 min 30 later</span>'),
 ('<span class="c"># le diagnostic du Doc, avec son coût</span>', '<span class="c"># the Doc\'s diagnosis, with its cost</span>'),
 ('>Copier le git clone</button>', '>Copy the git clone</button>'),
 ('<div class="sec-head"><p class="eyebrow">Le staff</p><h2>Neuf rôles. Trois piliers. Un deck.</h2><p class="lead">Sept agents IA et deux garde-fous, chacun avec son nom métier, son surnom et son matériel. Le Doc, le Ranger et l\'Intendant fonctionnent déjà ; les six autres rôles ne sont pas encore codés.</p></div>',
  '<div class="sec-head"><p class="eyebrow">The staff</p><h2>Nine roles. Three pillars. One deck.</h2><p class="lead">Seven AI agents and two guardrails, each with its job title, its nickname and its equipment. The Doc, the Ranger and the Intendant already work; the six other roles are not coded yet.</p></div>'),
 ('<p class="eyebrow">Les règles · la progression</p><h2>Au départ, personne n\'a les clés du camion.</h2><p class="lead">Mis à l\'épreuve, pas à pas : un agent gagne son autonomie comme un nouveau '
  "dans l'équipe, en faisant ses preuves action par action.</p>",
  '<p class="eyebrow">The rules · progression</p><h2>Nobody gets the keys on day one.</h2><p class="lead">Put to the test, step by step: an agent earns its autonomy like a newcomer to the team, by '
  'proving itself action by action.</p>'),
 ('<span class="n">niveau 1</span><h3>Apprenti</h3>', '<span class="n">level 1</span><h3>Apprentice</h3>'),
 ('<ul><li>lecture seule</li><li>à blanc ; votre historique, plus tard</li><li>diagnostics vers un canal de relecture</li></ul><span class="how">→ chaque agent arrive comme ça au <a href="#carte-campdebase">Camp de base</a></span>',
  '<ul><li>read-only</li><li>dry run; your history, later</li><li>diagnoses to a review channel</li></ul><span class="how">→ every agent arrives like this at <a href="#carte-campdebase">Base camp</a></span>'),
 ('<span class="n">niveau 2</span><h3>Compagnon</h3>', '<span class="n">level 2</span><h3>Practitioner</h3>'),
 ('<ul><li>travaille au quotidien</li><li>propose ; vos équipes valident et lancent</li><li>diagnostics vers l\'astreinte</li></ul><span class="how">→ devient Compagnon par une PR relue sur <code>spec.level</code></span>',
  '<ul><li>works day to day</li><li>proposes; your teams approve and launch</li><li>diagnoses to the on-call</li></ul><span class="how">→ becomes Practitioner through a reviewed PR on <code>spec.level</code></span>'),
 ('<span class="n">niveau 3</span><h3>Maître</h3>', '<span class="n">level 3</span><h3>Expert</h3>'),
 ('<ul><li>agit seul, action par action</li><li>rend compte après coup</li><li>jamais hors du cadre validé</li></ul><span class="how">→ devient Maître, geste par geste, par une PR relue sur <code>spec.level</code></span>',
  '<ul><li>acts alone, action by action</li><li>reports afterwards</li><li>never outside the validated frame</li></ul><span class="how">→ becomes Expert, one action at a time, through a reviewed PR on <code>spec.level</code></span>'),
 ('<p class="note"><b>Sous le capot :</b> le niveau est déclaré dans la ressource du rôle, champ <code>spec.level</code> (<code>Apprentice</code>, <code>Practitioner</code> ou <code>Expert</code>). '
  "L'opérateur en déduit les outils autorisés, et la passerelle les applique outil par outil. Changer de niveau, c'est modifier la ressource : une PR relue et validée par vos équipes, un changement "
  'tracé.</p>',
  '<p class="note"><b>Under the hood:</b> the level is declared in the role\'s resource, field <code>spec.level</code> (<code>Apprentice</code>, <code>Practitioner</code> or <code>Expert</code>). '
  'The operator derives the allowed tools from it, and the gateway enforces them tool by tool. Changing level means changing the resource: a PR reviewed and approved by your teams, a traced '
  'change.</p>'),
 ('<p class="eyebrow">Les règles · l\'encadrement</p><h2>Les mêmes règles pour tous les agents.</h2><p class="lead">Le pilier Encadrer : un agent et deux garde-fous. Les garde-fous n\'agissent '
  "jamais sur votre infra : le Ranger fixe ce que chaque agent a le droit d'appeler, le Psy vérifie après coup ce qu'il a fait. Le but : les appliquer aussi aux agents de vos éditeurs, en faisant "
  'passer leurs appels par la même passerelle (à venir).</p><p><a class="btn" href="#lexique">Le lexique complet ›</a></p>',
  '<p class="eyebrow">The rules · governance</p><h2>The same rules for every agent.</h2><p class="lead">The Govern pillar: one agent and two guardrails. The guardrails never act on your infra: the '
  "Ranger sets what each agent may call, the Psy checks afterwards what it did. The goal: apply them to your vendors' agents too, by routing their calls through the same gateway (coming).</p><p><a "
  'class="btn" href="#lexique">The full lexicon ›</a></p>'),
 ("<span><b>Et l'Intendant ?</b> Il est dans le pilier Encadrer, mais c'est un agent comme les autres : il tient la Carte, recense les agents et les connecteurs en circulation, et propose chaque entrée à vos équipes. Il passe donc lui aussi par le Ranger et le Psy.</span>",
  '<span><b>What about the Intendant?</b> It belongs to the Govern pillar, but it is an agent like the others: it keeps the Map, lists the agents and connectors in circulation, and proposes each entry to your teams. So it goes through the Ranger and the Psy too.</span>'),
 ('<div class="sec-head"><p class="eyebrow">La pile</p><h2>Des briques reconnues, assemblées.</h2><p class="lead">Pas de réinvention : TakeCare orchestre des projets open source et y ajoute les '
  'rôles, les paliers et le cadre.</p></div>',
  '<div class="sec-head"><p class="eyebrow">The stack</p><h2>Proven building blocks, put together.</h2><p class="lead">No reinventing: TakeCare orchestrates open source projects and adds the roles, '
  'the levels and the frame.</p></div>'),
 ('${[["kagent","kagent","moteur des agents"],["agentgateway","agentgateway","passerelle"],["kubernetes","Kubernetes","opérateur Go"],[null,"MCP","connecteurs"],[null,"OpenTelemetry","traces"],["git","Git","matériel validé"],["langfuse","Langfuse","à l\'étude"]]',
  '${[["kagent","kagent","agent engine"],["agentgateway","agentgateway","gateway"],["kubernetes","Kubernetes","Go operator"],[null,"MCP","connectors"],[null,"OpenTelemetry","traces"],["git","Git","validated equipment"],["langfuse","Langfuse","under study"]]'),
 ('href="#architecture">Voir l\'architecture ›</a>', 'href="#architecture">See the architecture ›</a>'),
 ('<div class="sec-head" id="feuille-de-route"><p class="eyebrow">Feuille de route</p><h2>Où on en est.</h2></div>', '<div class="sec-head" id="feuille-de-route"><p class="eyebrow">Roadmap</p><h2>Where we stand.</h2></div>'),
 ("<div><span>fait · v0.1.0</span><b>Le Doc</b><p>Diagnostic d'incident de bout en bout : alerte, enquête en lecture seule, diagnostic sourcé sur Slack, trace et coût ; images et charts publiés.</p></div>",
  '<div><span>done · v0.1.0</span><b>The Doc</b><p>Incident diagnosis end to end: alert, read-only investigation, sourced diagnosis on Slack, trace and cost; images and charts published.</p></div>'),
 ("<div><span>fait · v0.2.0</span><b>Le Ranger, la Carte, plusieurs clusters</b><p>Le Ranger : les mêmes règles pour tous les agents à la passerelle, journal, masquage, filtrage, plafonds, modèles autorisés. La Carte, lue par le Doc. Un cluster d'administration qui supervise plusieurs clusters de charge. Une route de modèle par rôle.</p></div>",
  '<div><span>done · v0.2.0</span><b>The Ranger, the Map, several clusters</b><p>The Ranger: the same rules for every agent at the gateway, journal, masking, filtering, caps, allowed models. The Map, read by the Doc. An administration cluster supervising several workload clusters. One model route per role.</p></div>'),
 ('<div class="now"><span>maintenant · v0.3.0-rc.1</span><b>L\'Intendant</b><p>La Carte tenue : l\'inventaire du cluster, la découverte par les connecteurs et une passe agent, des propositions que vos équipes relisent, et leur résumé posté quand elles changent.</p></div><div><span>ensuite</span><b>Une release de corrections, puis l\'interface en terminal</b><p>D\'abord les corrections, puis un terminal pour installer, suivre et relire TakeCare.</p></div><div><span>puis</span><b>Le Kiné, le Mécano Apprenti, le Psy</b><p>Une release par rôle, dans cet ordre.</p></div>',
  '<div class="now"><span>now · v0.3.0-rc.1</span><b>The Intendant</b><p>The Map kept: the cluster\'s inventory, discovery through the connectors and an agent pass, proposals your teams review, and their digest posted when they change.</p></div><div><span>next</span><b>A corrections release, then the terminal interface</b><p>First the corrections, then a terminal to install, follow and review TakeCare.</p></div><div><span>then</span><b>The Kiné, the Mécano at Apprentice, the Psy</b><p>One release per role, in that order.</p></div>'),
 ('<div><span>plus tard</span><b>Le Mécano au complet, puis le Guide, puis le Coach et le Nutri</b><p>Ordre de la feuille de route actuelle ; il peut bouger.</p></div>',
  '<div><span>later</span><b>The full Mécano, then the Guide, then the Coach and the Nutri</b><p>The current roadmap order; it may change.</p></div>'),
 ('<div><p class="eyebrow">Maintenu par Agaetis</p><p style="margin-top:.6rem">Notre métier, c\'est de faire tourner la production de nos clients. TakeCare, c\'est l\'outil qu\'on voulait avoir pour '
  'mieux le faire. On le construit en open source, et on accompagne ceux qui veulent le déployer.</p></div>\n'
  '    <a class="btn" href="${VITRINE}" target="_blank" rel="noopener">Se faire accompagner ↗</a>',
  '<div><p class="eyebrow">Maintained by Agaetis</p><p style="margin-top:.6rem">Our job is running our customers\' production. TakeCare is the tool we wanted to have to do it better. We build it in '
  'the open, and we support those who want to deploy it.</p></div>\n'
  '    <a class="btn" href="${VITRINE}" target="_blank" rel="noopener">Get support ↗</a>'),
 ('const FILTERS = [["all","Tout",null],["agent","Agents","--c-agent"],["guard","Garde-fous","--c-guard"],["exo","Exercices","--c-exo"],["mat","Matériel","--c-mat"],["lieu","Lieux","--c-lieu"]];',
  'const FILTERS = [["all","All",null],["agent","Agents","--c-agent"],["guard","Guardrails","--c-guard"],["exo","Exercises","--c-exo"],["mat","Equipment","--c-mat"],["lieu","Places","--c-lieu"]];'),
 ('<div class="dgh"><h3>Le staff</h3><span>9 rôles · 7 agents, 2 garde-fous</span></div>', '<div class="dgh"><h3>The staff</h3><span>9 roles · 7 agents, 2 guardrails</span></div>'),
 ('<div class="sec-head"><p class="eyebrow">Le deck · ${R.length} cartes</p><h2>Toutes les cartes du jeu.</h2><p class="lead">Neuf rôles font le travail ; les exercices les font progresser ; le matériel et les lieux sont ce qu\'ils partagent. Chaque carte a sa traduction côté cluster et porte sa version ; en gris, ce qui n\'est pas encore codé.</p></div>',
  '<div class="sec-head"><p class="eyebrow">The deck · ${R.length} cards</p><h2>Every card in the game.</h2><p class="lead">Nine roles do the work; the exercises make them progress; the equipment and places are what they share. Every card has its counterpart in the cluster and carries its version; greyed out: not coded yet. The card artwork is in French for now.</p></div>'),
 ('aria-label="Filtrer les cartes"', 'aria-label="Filter the cards"'),
 ('${grp("Les exercices","3 · ce que les rôles pratiquent ensemble",fam(["exo"]))}', '${grp("The exercises","3 · what the roles practise together",fam(["exo"]))}'),
 ('${grp("Le matériel","13 · ce que les rôles préparent et emportent",fam(["mat"]))}',
  '${grp("The equipment","13 · what the roles prepare and carry",fam(["mat"]))}'),
 ('${grp("Les lieux","2 · d\'où tout part",fam(["lieu"]))}', '${grp("The places","2 · where it all starts",fam(["lieu"]))}'),
 ('const state = r.since ? "Disponible depuis v"+r.since : r.next ? "Prévu · "+NEXT[r.next].toLowerCase() : "";',
  'const state = r.since ? "Available since v"+r.since : r.next ? "Planned · "+NEXT[r.next].toLowerCase() : "";'),
 ('const stats = [["Type",TY[tOf(r)][0]], r.p?["Pilier",PIL[r.p].name]:null, ["Nom métier",r.job], state?["État",`<a href="#feuille-de-route">${esc(state)}</a>`,true]:null, x.pass?["Passage",x.pass]:null, x.when?["Quand",x.when]:null].filter(Boolean);',
  'const stats = [["Type",TY[tOf(r)][0]], r.p?["Pillar",PIL[r.p].name]:null, ["Job",r.job], state?["State",`<a href="#feuille-de-route">${esc(state)}</a>`,true]:null, x.pass?["Promotion",x.pass]:null, x.when?["When",x.when]:null].filter(Boolean);'),
 ('const fam = r.fam==="role" ? "Les rôles" : FAM[r.fam][0]+"s";', 'const fam = ({role:"The roles",exo:"Exercises",mat:"Equipment",lieu:"Places"})[r.fam];'),
 ('<div class="crumb"><a href="#deck">Le deck</a><span>/</span><span>${r.fam==="role"?"Le staff":TY[tOf(r)][0]}</span><span>/</span><span>${i+1} sur ${list.length}</span></div>',
  '<div class="crumb"><a href="#deck">The deck</a><span>/</span><span>${r.fam==="role"?"The staff":TY[tOf(r)][0]}</span><span>/</span><span>${i+1} of ${list.length}</span></div>'),
 ('<p class="fsub" style="margin-top:.7rem">Retournez la carte : derrière, la ressource <code>${kd.kind}</code>.</p>`:`<span class="bim"><img src="${img(k)}" alt="Carte : ${esc(r.nick)}, ${esc(r.job)}">${st(k)}</span>`}',
  '<p class="fsub" style="margin-top:.7rem">Flip the card: behind it, the <code>${kd.kind}</code> resource.</p>`:`<span class="bim"><img src="${img(k)}" alt="Card: ${esc(r.nick)}, ${esc(r.job)}">${st(k)}</span>`}'),
 ('<div class="blk"><h2>Dans le jeu</h2>', '<div class="blk"><h2>In the game</h2>'),
 ('<div class="blk"><h2>Dans le cluster</h2>', '<div class="blk"><h2>In the cluster</h2>'),
 ('<div class="blk"><h2>En action</h2><div class="scene2"><span class="sc-t">// scène</span>', '<div class="blk"><h2>In action</h2><div class="scene2"><span class="sc-t">// scene</span>'),
 ('<div class="blk"><h2>Ses paliers</h2><div class="steps">${f.levels.map((l,j)=>`<div class="step"><span class="k">${["Apprenti","Compagnon","Maître"][j]} <code class="api">${API[j]}</code></span>',
  '<div class="blk"><h2>Its levels</h2><div class="steps">${f.levels.map((l,j)=>`<div class="step"><span class="k">${API[j]}</span>'),
 ('<div class="blk"><h2>Son matériel</h2>', '<div class="blk"><h2>Its equipment</h2>'),
 ('<h2>${r.fam==="role"?"Joue avec":"Avec"}</h2>', '<h2>${r.fam==="role"?"Plays with":"With"}</h2>'),
 ('<div class="sec-head"><p class="eyebrow">Lexique</p><h2>Du jeu au cluster.</h2><p class="lead">Le vocabulaire des cartes sert à parler du système sans jargon. Voici ce qu\'il y a derrière chaque '
  'mot. Les ressources elles-mêmes, YAML compris, sont dans la <a href="#reference">Référence</a>.</p></div>',
  '<div class="sec-head"><p class="eyebrow">Lexicon</p><h2>From the game to the cluster.</h2><p class="lead">The card vocabulary is a way to talk about the system without jargon. Here is what lies '
  'behind each word. The resources themselves, YAML included, are in the <a href="#reference">Reference</a>.</p></div>'),
 ("<thead><tr><th>Dans le jeu</th><th>Ce que c'est</th><th>Dans le cluster</th></tr></thead>", '<thead><tr><th>In the game</th><th>What it is</th><th>In the cluster</th></tr></thead>'),
 ('${[["lieu","Les lieux"],["mat","Le matériel"],["exo","Les exercices","de l\'outillage, sans ressource Kubernetes"]]',
  '${[["lieu","The places"],["mat","The equipment"],["exo","The exercises","tooling, no Kubernetes resource"]]'),
 ('<tr class="lgh"><th colspan="3">Hors deck</th></tr>', '<tr class="lgh"><th colspan="3">Off deck</th></tr>'),
 ('<div class="sec-head" style="margin-top:3.5rem"><p class="eyebrow">Les rôles</p><h2>Qui fait quoi, techniquement.</h2></div>',
  '<div class="sec-head" style="margin-top:3.5rem"><p class="eyebrow">The roles</p><h2>Who does what, technically.</h2></div>'),
 ('<thead><tr><th>Rôle</th><th>Type</th><th>Dans le cluster</th></tr></thead>', '<thead><tr><th>Role</th><th>Type</th><th>In the cluster</th></tr></thead>'),
 ('const mb = [["Doc · Kiné",["doc","kine"],"Un modèle puissant","Une enquête longue, beaucoup d\'outils à interroger, un raisonnement à tenir."],["Guide",["guide"],"Un modèle rapide","Des réponses '
  'tirées de vos procédures, éventuellement sur un modèle hébergé chez vous."],["Coach · Nutri",["coach","nutri"],"Un modèle économe","Des analyses régulières de chiffres, où le coût compte plus que '
  'la finesse."],["Psy",["psy"],"Un autre modèle","Celui qui juge les réponses ne doit pas être celui qui les a écrites."]];',
  'const mb = [["Doc · Kiné",["doc","kine"],"A powerful model","A long investigation, many tools to query, a line of reasoning to hold."],["Guide",["guide"],"A fast model","Answers drawn from your '
  'procedures, possibly on a model hosted on your premises."],["Coach · Nutri",["coach","nutri"],"A thrifty model","Regular analyses of figures, where cost matters more than '
  'finesse."],["Psy",["psy"],"A different model","The one judging the answers must not be the one that wrote them."]];'),
 ('<div class="sec-head"><p class="eyebrow">Architecture</p><h2>Tout passe par la passerelle.</h2><p class="lead">Neuf rôles, une seule plateforme. L\'opérateur traduit les ressources TakeCare, kagent fait tourner les agents, et tous leurs appels (modèles, outils, entre agents) traversent agentgateway. Le Ranger y pose les règles, le Psy relit les traces, l\'Intendant tient le matériel validé et la Carte.</p></div>',
  '<div class="sec-head"><p class="eyebrow">Architecture</p><h2>Everything goes through the gateway.</h2><p class="lead">Nine roles, a single platform. The operator translates the TakeCare resources, kagent runs the agents, and all their calls (models, tools, between agents) cross agentgateway. The Ranger sets the rules there, the Psy reads the traces, the Intendant keeps the validated equipment and the Map.</p></div>'),
 ('<p class="legend"><span><i class="dot live"></i>disponible</span><span><i class="dot"></i>pointillés : prévu</span></p>',
  '<p class="legend"><span><i class="dot live"></i>available</span><span><i class="dot"></i>dashed: planned</span></p>'),
 ('aria-label="Architecture de TakeCare Agents : déclencheurs, opérateur, agents kagent, agentgateway, modèles et connecteurs MCP, traces et matériel validé"',
  'aria-label="TakeCare Agents architecture: triggers, operator, kagent agents, agentgateway, models and MCP connectors, traces and validated equipment"'),
 ('text-anchor="start">DÉCLENCHEURS</text>', 'text-anchor="start">TRIGGERS</text>'),
 ('text-anchor="middle">une alerte</text>', 'text-anchor="middle">an alert</text>'),
 ('text-anchor="middle">une échéance</text>', 'text-anchor="middle">a deadline</text>'),
 ('text-anchor="middle">une question</text>', 'text-anchor="middle">a question</text>'),
 ('text-anchor="middle">traduit les ressources</text>', 'text-anchor="middle">translates the resources</text>'),
 ('text-anchor="start">Les agents</text><text class="ts" x="440" y="80" text-anchor="start">kagent · ils se parlent en A2A</text>',
  'text-anchor="start">The agents</text><text class="ts" x="440" y="80" text-anchor="start">kagent · they talk over A2A</text>'),
 ('text-anchor="middle">tâche</text>', 'text-anchor="middle">task</text>'),
 ('text-anchor="middle">résultat</text>', 'text-anchor="middle">result</text>'),
 ('text-anchor="middle">le seul point de contrôle</text><text class="tc" x="850" y="118" text-anchor="middle">appels de modèles</text><text class="tc" x="850" y="144" text-anchor="middle">appels '
  'd\'outils MCP</text><text class="tc" x="850" y="170" text-anchor="middle">appels entre agents</text>',
  'text-anchor="middle">the single control point</text><text class="tc" x="850" y="118" text-anchor="middle">model calls</text><text class="tc" x="850" y="144" text-anchor="middle">MCP tool '
  'calls</text><text class="tc" x="850" y="170" text-anchor="middle">agent-to-agent calls</text>'),
 ('text-anchor="middle">Balises du Ranger</text><text class="tl" x="850" y="240" text-anchor="middle">mêmes règles pour tous</text>',
  'text-anchor="middle">Ranger Markers</text><text class="tl" x="850" y="240" text-anchor="middle">same rules for all</text>'),
 ('text-anchor="middle">outils</text><text class="tl" x="734" y="168" text-anchor="middle">du niveau</text>',
  'text-anchor="middle">tools</text><text class="tl" x="734" y="168" text-anchor="middle">of the level</text>'),
 ('text-anchor="middle">Modèle par rôle</text><text class="ts" x="1085" y="88" text-anchor="middle">Mistral, Claude, OpenAI,</text><text class="ts" x="1085" y="106" text-anchor="middle">Gemini, '
  'vLLM… au choix</text>',
  'text-anchor="middle">Model per role</text><text class="ts" x="1085" y="88" text-anchor="middle">Mistral, Claude, OpenAI,</text><text class="ts" x="1085" y="106" text-anchor="middle">Gemini, vLLM… '
  'your choice</text>'),
 ('text-anchor="middle">Connecteurs MCP</text>', 'text-anchor="middle">MCP connectors</text>'),
 ('text-anchor="start">Traces OpenTelemetry</text><text class="ts" x="440" y="394" text-anchor="start">une par action, conventions GenAI, OTLP</text><text class="tp" x="440" y="418" '
  'text-anchor="start">relues par le Psy, avec un modèle juge</text>',
  'text-anchor="start">OpenTelemetry traces</text><text class="ts" x="440" y="394" text-anchor="start">one per action, GenAI conventions, OTLP</text><text class="tp" x="440" y="418" '
  'text-anchor="start">read by the Psy, with a judge model</text>'),
 ('text-anchor="start">Matériel validé et Carte</text><text class="ts" x="780" y="394" text-anchor="start">dans votre Git · Sacs, compétences, consignes, Carte</text><text class="tp" x="780" y="418" text-anchor="start">tenus par l\'Intendant, validés par vos équipes</text>',
  'text-anchor="start">Validated equipment and Map</text><text class="ts" x="780" y="394" text-anchor="start">in your Git · Bags, skills, prompts, Map</text><text class="tp" x="780" y="418" text-anchor="start">kept by the Intendant, validated by your teams</text>'),
 ('text-anchor="start">Dossards vérifiés</text>', 'text-anchor="start">Bibs checked</text>'),
 ('${br("kubernetes","opérateur")}<p>Traduit les ressources TakeCare en configuration des briques et en RBAC, déclenche le travail des agents et publie leurs résultats. Il n\'installe rien et ne raisonne pas. Un cluster d\'administration supervise plusieurs clusters de charge, chacun derrière son front mTLS. Des alertes d\'autres outils lui parviendront par le Talkie (prévu).</p>',
  '${br("kubernetes","operator")}<p>Translates the TakeCare resources into building-block configuration and RBAC, triggers the agents\' work and publishes their results. It installs nothing and does no reasoning. An administration cluster supervises several workload clusters, each behind its mTLS front. Alerts from other tools will reach it through the Talkie (planned).</p>'),
 ('<div class="layer"><div><b>Les agents</b><span>kagent, en A2A</span></div>', '<div class="layer"><div><b>The agents</b><span>kagent, over A2A</span></div>'),
 ("<p>kagent fait tourner les sept agents, et ils se parlent en A2A : le Kiné part des enquêtes du Doc, par exemple. Chacun a sa ressource, son niveau et son modèle. Disponibles : le Doc, et l'Intendant (son inventaire, ses propositions et leur résumé). Les assistants IA de vos équipes poseront leurs questions au Guide par la Corde (prévu).</p>",
  "<p>kagent runs the seven agents, and they talk over A2A: the Kiné starts from the Doc's investigations, for instance. Each has its resource, its level and its model. Available: the Doc, and the Intendant (its inventory, its proposals and their digest). Your teams' AI assistants will ask the Guide their questions through the Rope (planned).</p>"),
 ('<div class="layer hl"><div><b>agentgateway</b><span>le seul point de contrôle</span></div>', '<div class="layer hl"><div><b>agentgateway</b><span>the single control point</span></div>'),
 ("<p>Chaque appel de modèle (la clé du fournisseur n'est jamais exposée, le coût est mesuré), chaque appel d'outil (autorisé outil par outil), chaque appel entre agents. Le Ranger y pose les "
  'Balises : les mêmes règles pour tous les agents. Y faire passer aussi les agents de vos éditeurs est à venir.</p>',
  "<p>Every model call (the provider's key is never exposed, the cost is measured), every tool call (allowed tool by tool), every call between agents. The Ranger sets the Markers there: the same "
  "rules for every agent. Routing your vendors' agents through it too is coming.</p>"),
 ('<div><b>Le modèle</b><span>compatible OpenAI, au choix</span></div>', '<div><b>The model</b><span>OpenAI-compatible, your choice</span></div>'),
 ('<p>Tout fournisseur compatible OpenAI, ou votre vLLM, déclaré dans TakeCarePlatform. À ce jour, un seul modèle a été mesuré au banc : <code>gemini-3.7-flash</code>.</p>',
  '<p>Any OpenAI-compatible provider, or your vLLM, declared in TakeCarePlatform. So far, a single model has been measured on the bench: <code>gemini-3.7-flash</code>.</p>'),
 ('<div><b>Les connecteurs</b><span>TakeCareConnector, en MCP</span></div>', '<div><b>The connectors</b><span>TakeCareConnector, over MCP</span></div>'),
 ("<p>Tout ce qu'un agent lit ou écrit est un serveur MCP derrière la passerelle, la Carte comprise. Le niveau décide des outils autorisés ; la passerelle vérifie aussi les arguments d'un outil quand une règle le demande.</p>",
  "<p>Everything an agent reads or writes is an MCP server behind the gateway, the Map included. The level decides which tools are allowed; the gateway also checks a tool's arguments when a rule asks for it.</p>"),
 ('<div><b>Le matériel validé et la Carte</b><span>dans votre Git</span></div>',
  '<div><b>The validated equipment and the Map</b><span>in your Git</span></div>'),
 ("<p>L'Intendant recense les agents et les connecteurs en circulation, propose chaque entrée, que vos équipes transforment en pull request dans leur Git, et tient la Carte de l'infra. Seul ce que vos équipes ont validé équipe un agent (liste approuvée : prévue).</p>",
  '<p>The Intendant lists the agents and connectors in circulation, proposes each entry, which your teams turn into a pull request in their Git, and keeps the Map of the infra. Only what your teams have validated equips an agent (approved list: planned).</p>'),
 ('<div><b>La télémétrie</b><span>OpenTelemetry, dès le premier jour</span></div>', '<div><b>Telemetry</b><span>OpenTelemetry, from day one</span></div>'),
 ('${br(null,"Jaeger (démo)")}', '${br(null,"Jaeger (demo)")}'),
 ("<p>Une trace par action, selon les conventions GenAI. Le Psy les relit avec un modèle juge, jamais celui qu'il juge.</p>",
  '<p>One trace per action, following the GenAI conventions. The Psy reads them with a judge model, never the one it judges.</p>'),
 ('<p class="note">Le chart <code>takecare</code> installe l\'opérateur, ses CRD et les briques tierces, avec un profil <code>prod</code> ou <code>demo</code>. <b>Disponible :</b> l\'opérateur, le Doc, le Ranger, l\'Intendant, la Carte, la passerelle, les connecteurs et la télémétrie. Le reste est prévu. Les choix et leurs raisons sont dans les <a href="${REPO}/tree/main/docs/adr" target="_blank" rel="noopener">ADR du dépôt</a>.</p>',
  '<p class="note">The <code>takecare</code> chart installs the operator, its CRDs and the third-party building blocks, with a <code>prod</code> or <code>demo</code> profile. <b>Available:</b> the operator, the Doc, the Ranger, the Intendant, the Map, the gateway, the connectors and telemetry. The rest is planned. The choices and their reasons are in the <a href="${REPO}/tree/main/docs/adr" target="_blank" rel="noopener">repository\'s ADRs</a>.</p>'),
 ('<div class="sec-head" style="margin-top:4rem"><p class="eyebrow">Un modèle par rôle</p><h2>Chaque rôle a son travail, donc son modèle.</h2><p class="lead">Le modèle se choisit rôle par rôle (ADR '
  '0004), et la passerelle mesure le coût de chaque appel. Les profils ci-dessous sont une illustration, <b>à mesurer</b> : seule la règle du Psy est écrite dans les specs.</p></div>',
  '<div class="sec-head" style="margin-top:4rem"><p class="eyebrow">A model per role</p><h2>Each role has its job, so its model.</h2><p class="lead">The model is chosen role by role (ADR 0004), and '
  "the gateway measures the cost of every call. The profiles below are an illustration, <b>still to be measured</b>: only the Psy's rule is written in the specs.</p></div>"),
 ('<div class="sec-head"><p class="eyebrow">Getting started · mode démo</p><h2>Le Doc en action, sur votre poste.</h2><p class="lead">Le même produit, sur un cluster k3d local, avec un décor '
  "(<code>billing-api</code>) et un incident qu'on rejoue (<code>proxy-timeout</code>). La démo ne touche jamais à votre contexte kube : elle a son propre kubeconfig, "
  '<code>bin/takecare-demo.kubeconfig</code>.</p></div>',
  '<div class="sec-head"><p class="eyebrow">Getting started · demo mode</p><h2>The Doc in action, on your machine.</h2><p class="lead">The same product, on a local k3d cluster, with a scenery '
  '(<code>billing-api</code>) and an incident we replay (<code>proxy-timeout</code>). The demo never touches your kube context: it has its own kubeconfig, '
  '<code>bin/takecare-demo.kubeconfig</code>.</p></div>'),
 ('<span class="c"># une clé de modèle, ou un fichier .env ignoré par git</span>', '<span class="c"># a model key, or a .env file ignored by git</span>'),
 ('<span class="c"># cluster, TakeCare, décor, Doc</span>', '<span class="c"># cluster, TakeCare, scenery, Doc</span>'),
 ('<span class="c"># le diagnostic et sa consommation</span>', '<span class="c"># the diagnosis and its usage</span>'),
 ('<span class="c"># le banc de précision, sur l\'incident en cours</span>', '<span class="c"># the accuracy bench, on the current incident</span>'),
 ('<span class="c"># le décor remis en état</span>', '<span class="c"># the scenery restored</span>'),
 ('<span class="c"># on range tout</span>', '<span class="c"># tidy everything up</span>'),
 ('<p class="note">Les images et les charts de la dernière release sont sur <code>ghcr.io/agaetis-it</code> ; <code>DEMO_IMAGES=build</code> construit les images depuis le code et les importe dans le cluster. Un second <code>make demo-up</code> ne change rien et prend quelques secondes.</p>',
  '<p class="note">The images and charts of the latest release are on <code>ghcr.io/agaetis-it</code>; <code>DEMO_IMAGES=build</code> builds the images from the code and imports them into the cluster. A second <code>make demo-up</code> changes nothing and takes a few seconds.</p>'),
 ('<p class="eyebrow" style="margin-bottom:.8rem">Prérequis</p><ul class="todo" style="--x:0"><li>Docker, avec au moins 5 Go de mémoire (la pile en prend 3,5 Gio au repos et 4,1 Gio pendant un incident)</li><li>k3d, kubectl, make</li><li>Helm 4 : téléchargé par le Makefile</li><li>Une clé pour un modèle compatible OpenAI</li></ul>',
  '<p class="eyebrow" style="margin-bottom:.8rem">Prerequisites</p><ul class="todo" style="--x:0"><li>Docker, with at least 5 GB of memory (the stack uses 3.5 GiB idle and 4.1 GiB during an incident)</li><li>k3d, kubectl, make</li><li>Helm 4: downloaded by the Makefile</li><li>A key for an OpenAI-compatible model</li></ul>'),
 ('<p class="eyebrow" style="margin-bottom:.8rem">En option : Slack</p><p style="color:var(--mute);font-size:.94rem">Avec <code>TAKECARE_SLACK_BOT_TOKEN</code> et '
  "<code>TAKECARE_SLACK_ONCALL_CHANNEL</code> (et <code>TAKECARE_SLACK_REVIEW_CHANNEL</code> pour le canal de relecture d'un Apprenti), les diagnostics partent sur Slack. Sans, ils restent dans le "
  "journal du serveur de publication, et <code>make demo-verdict</code> les lit dans l'enquête.</p>",
  '<p class="eyebrow" style="margin-bottom:.8rem">Optional: Slack</p><p style="color:var(--mute);font-size:.94rem">With <code>TAKECARE_SLACK_BOT_TOKEN</code> and '
  "<code>TAKECARE_SLACK_ONCALL_CHANNEL</code> (and <code>TAKECARE_SLACK_REVIEW_CHANNEL</code> for an Apprentice's review channel), diagnoses go to Slack. Without them, they stay in the publishing "
  "server's log, and <code>make demo-verdict</code> reads them from the investigation.</p>"),
 ('<p class="eyebrow" style="margin-bottom:.8rem">En production</p><p style="color:var(--mute);font-size:.94rem">Deux charts : <a href="${REPO}/blob/main/charts/takecare-crds/README.md" target="_blank" rel="noopener"><code>takecare-crds</code></a> d\'abord (jamais désinstallé tant que TakeCare tourne), puis <a href="${REPO}/blob/main/charts/takecare/README.md" target="_blank" rel="noopener"><code>takecare</code></a> avec <code>values-prod.yaml</code>, chaque valeur à fournir marquée <code>REQUIRED</code>. Lisez le README de chaque chart avant d\'installer ; les charts sont publiés depuis la 0.1.0.</p>',
  '<p class="eyebrow" style="margin-bottom:.8rem">In production</p><p style="color:var(--mute);font-size:.94rem">Two charts: <a href="${REPO}/blob/main/charts/takecare-crds/README.md" target="_blank" rel="noopener"><code>takecare-crds</code></a> first (never uninstalled while TakeCare runs), then <a href="${REPO}/blob/main/charts/takecare/README.md" target="_blank" rel="noopener"><code>takecare</code></a> with <code>values-prod.yaml</code>, every value to provide marked <code>REQUIRED</code>. Read each chart\'s README before installing; the charts are published since 0.1.0.</p>'),
 ('target="_blank" rel="noopener">Le README, à jour ↗</a>', 'target="_blank" rel="noopener">The README, up to date ↗</a>'),
 ('sub:"Le Camp de base : modèles, passerelle, moteur, télémétrie. Une seule instance, rendue par le chart. Le matériel des agents vient du Git du client, par une liste approuvée (ADR 0019, prévue)."',
  'sub:"Base camp: models, gateway, engine, telemetry. A single instance, rendered by the chart. The agents\' equipment comes from the customer\'s Git, through an approved list (ADR 0019, planned)."'),
 ('sub:"Le Doc : son niveau, son périmètre, ses connecteurs, où partent ses diagnostics."', 'sub:"The Doc: its level, its scope, its connectors, where its diagnoses go."'),
 ('sub:"Une enquête par alerte : sa phase, son diagnostic, son coût et sa trace."', 'sub:"One investigation per alert: its phase, its diagnosis, its cost and its trace."'),
 ('sub:"Un accès du Sac : un serveur MCP derrière la passerelle, avec ses outils classés read, propose ou act."',
  'sub:"One access in the Bag: an MCP server behind the gateway, with its tools classed read, propose or act."'),
 ('sub:"Le Ranger : son niveau, et de quoi sont faites ses Balises : masquage, filtrage, plafonds, modèles autorisés."', 'sub:"The Ranger: its level, and what its Markers are made of: masking, filtering, caps, allowed models."'),
 ('sub:"Le Terrain d\'entraînement de la démo : le décor billing-api et l\'incident à rejouer."', 'sub:"The demo\'s Training ground: the billing-api scenery and the incident to replay."'),
 ('aria-label="Retourner la carte ${x.kind}"', 'aria-label="Flip the ${x.kind} card"'),
 ('data-kind="${x.kind}">Copier</button>', 'data-kind="${x.kind}">Copy</button>'),
 ('href="#champs-${x.kind}">Champs ›</a>', 'href="#champs-${x.kind}">Fields ›</a>'),
 ('<div class="sec-head"><p class="eyebrow">Référence · takecare.agaetis.fr/v1alpha1</p><h2>Retournez la carte : derrière, la ressource.</h2><p class="lead">Chaque ressource Kubernetes a sa carte. '
  "Cliquez pour voir son YAML, tiré des specs du dépôt. L'API est en <code>v1alpha1</code> : elle bougera.</p></div>",
  '<div class="sec-head"><p class="eyebrow">Reference · takecare.agaetis.fr/v1alpha1</p><h2>Flip the card: behind it, the resource.</h2><p class="lead">Every Kubernetes resource has its card. Click '
  "to see its YAML, taken from the repository's specs. The API is <code>v1alpha1</code>: it will change.</p></div>"),
 ('<div class="sec-head" style="margin-top:4rem"><p class="eyebrow">Prévues</p><h2>Les prochaines cartes de l\'API.</h2><p class="lead">Une ressource par rôle, sur le même modèle que TakeCareDoc. '
  "Leur dos s'écrira avec leur spec.</p></div>",
  '<div class="sec-head" style="margin-top:4rem"><p class="eyebrow">Planned</p><h2>The next cards of the API.</h2><p class="lead">One resource per role, on the same model as TakeCareDoc. Their back '
  'will be written with their spec.</p></div>'),
 ('f.r?\'<span class="rq">requis</span>\':""', 'f.r?\'<span class="rq">required</span>\':""'),
 ('`<span class="df">défaut <code>${esc(fv(f.def))}</code></span>`', '`<span class="df">default <code>${esc(fv(f.def))}</code></span>`'),
 ('<p class="eyebrow">Les champs</p><h2>Champ par champ, ressource par ressource.</h2><p class="lead">Tiré des CRD du dépôt : ce que l\'API accepte vraiment, avec ses valeurs permises et ses '
  'défauts. Les descriptions sont celles du code, en anglais.</p>',
  '<p class="eyebrow">The fields</p><h2>Field by field, resource by resource.</h2><p class="lead">Taken from the repository\'s CRDs: what the API really accepts, with its allowed values and its '
  "defaults. The descriptions are the code's own.</p>"),
 ('aria-label="Ressources"', 'aria-label="Resources"'),
 ('<dl><div><dt>Portée</dt><dd>${x.scope==="Cluster"?"cluster":"namespace"}</dd></div><div><dt>Nom court</dt>',
  '<dl><div><dt>Scope</dt><dd>${x.scope==="Cluster"?"cluster":"namespace"}</dd></div><div><dt>Short name</dt>'),
 ('<div><dt>Catégories</dt>', '<div><dt>Categories</dt>'),
 ('<thead><tr><th>Champ</th><th>Type</th><th>Description</th></tr></thead>', '<thead><tr><th>Field</th><th>Type</th><th>Description</th></tr></thead>'),
 ("<summary>Le statut, écrit par l'opérateur <span>${x.status.length} champs</span></summary>", '<summary>The status, written by the operator <span>${x.status.length} fields</span></summary>'),
 ('\'<span class="rq">requis en prod</span>\'', '\'<span class="rq">required in prod</span>\''),
 ('<p class="eyebrow">Les charts</p><h2>Les valeurs, une par une.</h2><p class="lead">Deux charts : <code>takecare-crds</code> d\'abord, puis <code>takecare</code>. En production, '
  '<code>values-prod.yaml</code> refuse de s\'installer tant qu\'une valeur marquée <span class="rq">requis en prod</span> manque.</p>',
  '<p class="eyebrow">The charts</p><h2>The values, one by one.</h2><p class="lead">Two charts: <code>takecare-crds</code> first, then <code>takecare</code>. In production, '
  '<code>values-prod.yaml</code> refuses to install while a value marked <span class="rq">required in prod</span> is missing.</p>'),
 ('<span class="sr">Filtrer les valeurs</span><input type="search" id="rq" placeholder="Filtrer : slack, otlp, proxy…" autocomplete="off">',
  '<span class="sr">Filter the values</span><input type="search" id="rq" placeholder="Filter: slack, otlp, proxy…" autocomplete="off">'),
 ('<thead><tr><th>Clé</th><th>Type et défaut</th><th>Description</th></tr></thead>', '<thead><tr><th>Key</th><th>Type and default</th><th>Description</th></tr></thead>'),
 ('<p class="note">Généré depuis le dépôt, commit <code>${esc(REF.commit.split("|")[0])}</code> du ${new '
  'Date(REF.commit.split("|")[1]).toLocaleDateString("fr-FR",{day:"numeric",month:"long",year:"numeric"})}. Pour tout le contexte (prérequis, profils, exigences de sécurité), lisez le README de '
  'chaque chart :',
  '<p class="note">Generated from the repository, commit <code>${esc(REF.commit.split("|")[0])}</code> of ${new '
  'Date(REF.commit.split("|")[1]).toLocaleDateString("en-GB",{day:"numeric",month:"long",year:"numeric"})}. For the full context (prerequisites, profiles, security requirements), read each chart\'s '
  'README:'),
 ("Et <code>kubectl get takecare -A</code> liste tout le staff, avec le niveau et l'état de chaque rôle.</p>",
  "And <code>kubectl get takecare -A</code> lists the whole staff, with each role's level and state.</p>"),
 ('c.textContent="Copié ✓"', 'c.textContent="Copied ✓"'),
 ('c.textContent="Copier"', 'c.textContent="Copy"'),
 ('<div class="sec-head"><p class="eyebrow">Contribuer</p><h2>Un projet maison, qui a besoin de vous pour grandir.</h2><p class="lead">On n\'est pas éditeur : TakeCare grandira avec ceux qui '
  "l'utilisent. Les specs et les décisions s'écrivent et se relisent avant le code, et tout se fait en PR relue.</p></div>",
  '<div class="sec-head"><p class="eyebrow">Contribute</p><h2>A home-grown project that needs you to grow.</h2><p class="lead">We are not a software vendor: TakeCare will grow with the people who '
  'use it. Specs and decisions are written and reviewed before the code, and everything goes through reviewed PRs.</p></div>'),
 ('<div class="panel"><b>Pour commencer</b><p>Trois issues prêtes à prendre, marquées « good first issue ».</p>',
  '<div class="panel"><b>To get started</b><p>Three issues ready to pick up, labelled "good first issue".</p>'),
 ('${iss(69,"Démo : le scénario bad-config-rollout")}${iss(70,"Démo : le scénario missing-secret-key")}${iss(71,"Lancer la démo depuis les images publiées, sur Linux amd64")}',
  '${iss(69,"Demo: the bad-config-rollout scenario")}${iss(70,"Demo: the missing-secret-key scenario")}${iss(71,"Run the demo from the published images, on Linux amd64")}'),
 ('target="_blank" rel="noopener">Toutes les issues ↗</a>', 'target="_blank" rel="noopener">All issues ↗</a>'),
 ('<div class="panel"><b>Brancher un nouvel outil</b><p>Tout passe par des connecteurs MCP derrière la passerelle : un serveur pour votre outil de tickets, de supervision ou votre cloud élargit le '
  'terrain de tous les agents.</p><ul class="issues">${iss(72,"Un connecteur de logs (Loki)","help wanted")}',
  '<div class="panel"><b>Plug in a new tool</b><p>Everything goes through MCP connectors behind the gateway: a server for your ticketing tool, your monitoring or your cloud widens the terrain of '
  'every agent.</p><ul class="issues">${iss(72,"A logs connector (Loki)","help wanted")}'),
 ('target="_blank" rel="noopener">La spec des connecteurs ↗</a>', 'target="_blank" rel="noopener">The connectors spec ↗</a>'),
 ('<div class="panel"><b>Proposer un scénario de panne</b><p>Une panne que vous connaissez par cœur peut devenir un scénario rejoué, comme <code>proxy-timeout</code> dans la démo. C\'est aussi ce '
  'qui nourrit le banc de précision du Doc.</p>',
  '<div class="panel"><b>Propose a failure scenario</b><p>A failure you know by heart can become a replayed scenario, like <code>proxy-timeout</code> in the demo. It also feeds the Doc\'s accuracy '
  'bench.</p>'),
 ('target="_blank" rel="noopener">La spec de la démo ↗</a>', 'target="_blank" rel="noopener">The demo spec ↗</a>'),
 ('<div class="panel"><b>Lire avant d\'écrire</b><p>Les specs (<code>docs/specs/</code>), les décisions (<code>docs/adr/</code> : une décision qui change donne un nouvel ADR, jamais une réécriture) '
  'et <code>AGENTS.md</code>, le règlement du dépôt. Tout est en anglais, sauf les noms des rôles.</p>',
  '<div class="panel"><b>Read before you write</b><p>The specs (<code>docs/specs/</code>), the decisions (<code>docs/adr/</code>: a decision that changes gets a new ADR, never a rewrite) and '
  "<code>AGENTS.md</code>, the repository's rulebook. Everything is in English, except the role names.</p>"),
 ('target="_blank" rel="noopener">Les specs ↗</a><a class="btn" href="${REPO}/tree/main/docs/adr" target="_blank" rel="noopener">Les ADR ↗</a>',
  'target="_blank" rel="noopener">The specs ↗</a><a class="btn" href="${REPO}/tree/main/docs/adr" target="_blank" rel="noopener">The ADRs ↗</a>'),
 ('<div class="sec-head" style="margin-top:3rem"><p class="eyebrow">Développer</p><h2>Tout passe par <code class="h2c">make</code>.</h2><p class="lead">Go, Docker et make suffisent : chaque version '
  "est épinglée dans <code>hack/versions.env</code>, les outils s'installent dans <code>./bin</code>, et Helm y tourne avec sa propre configuration, jamais la vôtre.</p></div>",
  '<div class="sec-head" style="margin-top:3rem"><p class="eyebrow">Develop</p><h2>Everything goes through <code class="h2c">make</code>.</h2><p class="lead">Go, Docker and make are enough: every '
  'version is pinned in <code>hack/versions.env</code>, the tools install into <code>./bin</code>, and Helm runs there with its own configuration, never yours.</p></div>'),
 ('<span class="c"># toutes les cibles</span>', '<span class="c"># every target</span>'),
 ('<span class="c"># unitaires, golden, envtest, charts</span>', '<span class="c"># unit, golden, envtest, charts</span>'),
 ('<span class="c"># golangci-lint, shellcheck, markdownlint, en-têtes SPDX</span>', '<span class="c"># golangci-lint, shellcheck, markdownlint, SPDX headers</span>'),
 ('<span class="c"># dérive des charts + helm lint, deux profils</span>', '<span class="c"># chart drift + helm lint, two profiles</span>'),
 ('<span class="c"># licences des dépendances, NOTICE régénéré</span>', '<span class="c"># dependency licenses, NOTICE regenerated</span>'),
 ('<span class="c"># recherche de secrets</span>', '<span class="c"># secret scanning</span>'),
 ('<p class="note"><b>Les règles du jeu :</b> <a href="${REPO}/blob/main/CONTRIBUTING.md" target="_blank" rel="noopener">CONTRIBUTING.md</a> pour l\'environnement et la revue, avec signature DCO. '
  'Une faille ? Signalez-la en privé, comme l\'explique <a href="${REPO}/blob/main/SECURITY.md" target="_blank" rel="noopener">SECURITY.md</a>. Chacun suit le <a '
  'href="${REPO}/blob/main/CODE_OF_CONDUCT.md" target="_blank" rel="noopener">code de conduite</a>. Licence Apache 2.0, copyright Agaetis ; les règles de provenance (en-têtes SPDX, pas de copyleft '
  'fort) sont dans <code>docs/licensing.md</code>.</p>',
  '<p class="note"><b>The ground rules:</b> <a href="${REPO}/blob/main/CONTRIBUTING.md" target="_blank" rel="noopener">CONTRIBUTING.md</a> for the environment and review, with DCO sign-off. Found a '
  'vulnerability? Report it privately, as <a href="${REPO}/blob/main/SECURITY.md" target="_blank" rel="noopener">SECURITY.md</a> explains. Everyone follows the <a '
  'href="${REPO}/blob/main/CODE_OF_CONDUCT.md" target="_blank" rel="noopener">code of conduct</a>. Apache 2.0 license, copyright Agaetis; the provenance rules (SPDX headers, no strong copyleft) are '
  'in <code>docs/licensing.md</code>.</p>'),
 ('pick(h,\'<p class="eyebrow">La pile</p>\').replace(\'<section>\',\'<section class="tight">\') + pick(h,\'Maintenu par Agaetis\');',
  'pick(h,\'<p class="eyebrow">The stack</p>\').replace(\'<section>\',\'<section class="tight">\') + pick(h,\'Maintained by Agaetis\');'),
 ('cp.textContent="Copié"', 'cp.textContent="Copied"'),
 ('cp.textContent="Sélectionné"', 'cp.textContent="Selected"'),
 ('c.textContent="Sélectionné"', 'c.textContent="Selected"'),
 ('<a class="lang" id="lang" href="__OTHERLANG__" hreflang="en" lang="en" title="English version">EN</a>',
  '<a class="lang" id="lang" href="__OTHERLANG__" hreflang="fr" lang="fr" title="Version française">FR</a>'),
 ('<a href="#mentions">Mentions légales</a></div>', '<a href="#mentions">Legal notice</a></div>'),
 ('    <div class="sec-head"><p class="eyebrow">Mentions légales</p><h2>Qui publie ce site.</h2><p class="lead">Le site du projet open source TakeCare Agents. Il ne collecte aucune donnée '
  'personnelle.</p></div>',
  '    <div class="sec-head"><p class="eyebrow">Legal notice</p><h2>Who publishes this site.</h2><p class="lead">The site of the TakeCare Agents open source project. It collects no personal '
  'data.</p></div>'),
 ('${b("Éditeur",`<p><b>AGAETIS</b>, société par actions simplifiée au capital de 166&nbsp;100&nbsp;€<br>9 allée Évariste Galois, 63170 Aubière, France<br>RCS Clermont-Ferrand 490&nbsp;280&nbsp;435 '
  '· TVA intracommunautaire FR53490280435<br>Téléphone : 04&nbsp;73&nbsp;35&nbsp;47&nbsp;51 · <a href="mailto:contact@agaetis.fr">contact@agaetis.fr</a></p>`)}',
  '${b("Publisher",`<p><b>AGAETIS</b>, a French simplified joint-stock company (SAS) with a share capital of €166,100<br>9 allée Évariste Galois, 63170 Aubière, France<br>Registered with the '
  'Clermont-Ferrand Trade and Companies Register (RCS) under number 490 280 435 · EU VAT FR53490280435<br>Phone: +33 4 73 35 47 51 · <a '
  'href="mailto:contact@agaetis.fr">contact@agaetis.fr</a></p>`)}'),
 ('${b("Directeur de la publication",`<p>Nicolas Roux, directeur général d\'Agaetis.</p>`)}', '${b("Publication director",`<p>Nicolas Roux, Managing Director (directeur général) of Agaetis.</p>`)}'),
 ('${b("Hébergeur",`<p><b>GitHub, Inc.</b> (GitHub Pages)<br>88 Colin P Kelly Jr St, San Francisco, CA 94107, États-Unis<br>',
  '${b("Host",`<p><b>GitHub, Inc.</b> (GitHub Pages)<br>88 Colin P Kelly Jr St, San Francisco, CA 94107, United States<br>'),
 ('${b("Données personnelles",`<p>Ce site ne dépose aucun cookie, ne mesure pas l\'audience et ne contient aucun formulaire. Les polices, les images et la vidéo sont servies par le site lui-même. '
  'Comme tout hébergeur, GitHub enregistre techniquement les connexions au site (adresse IP), selon sa propre <a '
  'href="https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement" target="_blank" rel="noopener">déclaration de confidentialité ↗</a>. Pour toute question : <a '
  'href="mailto:contact@agaetis.fr">contact@agaetis.fr</a>.</p>`)}',
  '${b("Personal data",`<p>This site sets no cookies, measures no audience and has no forms. Fonts, images and video are served by the site itself. Like any host, GitHub technically logs connections '
  'to the site (IP address), under its own <a href="https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement" target="_blank" rel="noopener">privacy statement ↗</a>. Any '
  'question: <a href="mailto:contact@agaetis.fr">contact@agaetis.fr</a>.</p>`)}'),
 ('${b("Propriété intellectuelle",`<p>Le logiciel TakeCare Agents est open source, sous <a href="${REPO}/blob/main/LICENSE" target="_blank" rel="noopener">licence Apache 2.0 ↗</a>, copyright '
  "Agaetis. Les textes, illustrations, vidéos et le nom TakeCare appartiennent à Agaetis ; les illustrations ont été réalisées pour Agaetis avec l'aide d'outils d'IA. Les logos et marques cités "
  'appartiennent à leurs propriétaires.</p>`)}',
  '${b("Intellectual property",`<p>The TakeCare Agents software is open source, under the <a href="${REPO}/blob/main/LICENSE" target="_blank" rel="noopener">Apache 2.0 license ↗</a>, copyright '
  'Agaetis. The texts, illustrations, videos and the TakeCare name belong to Agaetis; the illustrations were made for Agaetis with the help of AI tools. The logos and brands mentioned belong to '
  'their owners.</p>`)}'),
 (' talkie:"Un add-on de TakeCare (ADR 0018) : chaque source d\'alertes est convertie puis poussée dans l\'Alertmanager de TakeCare, avec les étiquettes qui permettent au Doc de regrouper et de router. Pas encore codé.",\n corde:"Un outil « demander au Guide », servi par un serveur MCP derrière la passerelle (en A2A pour les agents qui le parlent). Chaque assistant a son Dossard ; aucun outil d\'action ne passe par la Corde. Pas encore spécifié.",\n',
  ' talkie:"A TakeCare add-on (ADR 0018): each alert source is converted, then pushed into TakeCare\'s Alertmanager, with the labels that let the Doc group and route. Not coded yet.",\n corde:"An “ask the Guide” tool, served by an MCP server behind the gateway (over A2A for the agents that speak it). Each assistant has its Bib; no action tool goes through the Rope. Not specified yet.",\n'),
 ('text-anchor="middle">Talkie · vos outils</text>',
  'text-anchor="middle">Talkie · your tools</text>'),
 ('text-anchor="middle">Corde · assistants</text>',
  'text-anchor="middle">Rope · assistants</text>'),
 ('text-anchor="middle">au Guide</text>',
  'text-anchor="middle">to the Guide</text>'),
 ('<span class="c"># le banc de l\'Intendant, sur la démo</span>',
  '<span class="c"># the Intendant\'s bench, on the demo</span>')]
