<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/takecare-logo-blanc.svg">
    <img src="docs/assets/takecare-logo.svg" alt="TakeCare Agents by Agaetis" width="360">
  </picture>
</p>

<p align="center"><b>Le staff AIOps qui prend soin de votre production.</b><br>
<i>Toujours là, jamais à votre place.</i></p>

---

TakeCare Agents est un staff d'agents IA, d'exercices et de garde-fous qui préparent, soignent et protègent une production Kubernetes. Il s'installe chez le client sous la forme d'un **opérateur Kubernetes**, sur une pile open source (agentgateway, HolmesGPT, puis kagent), avec le modèle de son choix. Un **mode démo** intégré monte le même produit sur un cluster local, avec un décor et des incidents rejouables.

> **Statut : en construction.** Le jalon 1 (socle, Doc, mode démo) est spécifié dans [`docs/`](docs/) ; le code arrive au fil des PR. Rien n'est encore installable.

## Le staff

Onze rôles, rangés en trois piliers. Chaque rôle commence par un essai, sans rien toucher.

| Préparer | Soigner | Protéger |
|---|---|---|
| **Coach** · Un pic d'activité en vue ? | **Doc** · Un incident en pleine nuit ? | **Ranger** · Vos agents tentés par le hors-piste ? |
| **Nutri** · Une facture cloud qui grimpe sans prévenir ? | **Kiné** · Une douleur qui revient à chaque incident ? | **Checkpoint** · Des agents et des outils qui arrivent de partout ? |
| **Guide** · Votre équipe doit monter en compétence ? | **Mécano** · Une montée de version que tout le monde repousse ? | **Psy** · Des agents qui inventent des chemins ou tournent en rond ? |
| **Bootcamp** · Votre plan B n'a jamais servi ? | | **Sparring** · Vos agents tiendraient-ils face à une attaque ? |

Agents : Coach, Nutri, Guide, Doc, Kiné, Mécano. Exercices menés par Agaetis : Bootcamp, Sparring. Garde-fous : Ranger, Checkpoint, Psy.

Le jalon 1 livre le **Doc** : à chaque alerte, il mène l'enquête en lecture seule et publie dans Slack un diagnostic sourcé (la cause, les pistes écartées, qui doit agir). Vous décidez du traitement.

## Le principe : le palier se déclare

Chaque rôle est une ressource Kubernetes qui porte son **palier d'intervention**. L'opérateur en déduit ce que le rôle a le droit de faire.

| Palier | Valeur | Ce que fait le rôle |
|---|---|---|
| À l'essai | `trial` | Il analyse, sur votre historique ou à blanc, sans rien toucher |
| En service | `service` | Il travaille au quotidien et propose ; vous validez et lancez |
| Aguerri | `seasoned` | Il agit seul, dans le cadre que vous avez validé (après le jalon 1) |

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
metadata:
  name: doc
  namespace: takecare
spec:
  level: service              # trial | service | seasoned
  scope:
    namespaces: [facturation]
  channel: astreinte          # canal déclaré dans la TakeCarePlatform
```

Changer de palier, c'est modifier cette ressource : une PR relue, un changement tracé. `kubectl get takecare -A` liste tout le staff, avec le rôle, le palier et l'état de chacun.

## Architecture du jalon 1

```
Alertmanager ──webhook──▶ takecare-operator ──HealthCheck──▶ HolmesGPT ──▶ Slack
                              │                                 │
                              │ génère config et RBAC           ├─ Kubernetes, Prometheus (lecture)
                              ▼                                 └─ sources MCP : post-mortems, runbooks…
                       agentgateway ◀──── tous les appels au modèle (coût, traces)
                              │
                              ▼
                 modèle compatible OpenAI (au choix du client)
```

- **`takecare-operator`** (Go, kubebuilder) traduit les ressources TakeCare en configuration des briques et en RBAC. Il n'installe rien et n'enquête pas lui-même.
- **Le chart `takecare`** installe l'opérateur, ses CRD et les briques tierces, en profil `prod` ou `demo`.
- **agentgateway** reçoit tous les appels au modèle : la clé du fournisseur n'est jamais exposée aux agents, et le coût de chaque enquête est mesuré.

Les choix et leurs raisons sont dans les [ADR](docs/adr/).

## Mode démo (prévu au jalon 1)

```bash
export TAKECARE_MODEL_API_KEY=...     # clé du fournisseur de modèle, jamais écrite dans le dépôt
make demo-up          # cluster k3d + TakeCare en profil démo + décor facturation-api
make demo-incident    # déclenche la panne ; l'alerte part environ 2 min 30 plus tard
make demo-bench N=5   # rejoue N enquêtes et compte les bons diagnostics
make demo-reset       # remet le décor en état
make demo-down        # supprime le cluster
```

Prérequis : Docker (4 Go de mémoire au moins), k3d, kubectl, helm, make.

## Documentation

| Document | Contenu |
|---|---|
| [`docs/specs/00-vision-jalon-1.md`](docs/specs/00-vision-jalon-1.md) | Vision, principes, périmètre et critères de sortie du jalon 1 |
| [`docs/adr/`](docs/adr/) | Décisions d'architecture |

## Comment on travaille

Les specs et les décisions sont écrites et relues avant le code. Chaque spec donne lieu à une PR, relue avant fusion. Une décision qui change donne un nouvel ADR, jamais une réécriture de l'ancien.

## Licence

Dépôt privé. La licence sera choisie avant toute ouverture.

---

<p align="center"><i>Comme un cairn sur le sentier, nos agents balisent votre chemin.</i><br>
TakeCare Agents by <a href="https://www.agaetis.fr">Agaetis</a></p>
