#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
"""Builds the product site into site/dist: /fr/ and /en/, the root redirecting
to the default language (French for now), assets shared under /assets/.

    python3 site/src/build.py [--out site/dist] [--default fr] [--site-url URL]

The site is served at its GitHub Pages address for now; --site-url switches
the canonical and social links to a custom domain later.
"""
import argparse, json, pathlib, re, shutil, sys
SRC = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(SRC))
import gen_ref, i18n_en

SHOWCASE = 'https://takecare.agaetis.fr'
SITE = 'https://agaetis-it.github.io/takecare-agents'
META = {
 'fr': ('fr', 'TakeCare Agents · des agents SRE à l\'épreuve du terrain',
        "Opérateur Kubernetes open source : neuf rôles d'agents IA pour l'exploitation, qui gagnent leur autonomie pas à pas, derrière une seule passerelle."),
 'en': ('en', 'TakeCare Agents · SRE agents, proven in the field',
        'Open source Kubernetes operator: nine AI agent roles for operations that earn their autonomy step by step, behind a single gateway.'),
}
FONTS = ''.join(
    f"@font-face{{font-family:'{fam}';font-style:{st};font-weight:{w};font-display:swap;src:url(../assets/fonts/{f}.woff2) format('woff2')}}\n"
    for fam, f, w, st in [('Poppins', f'poppins-latin-{w}-normal', w, 'normal') for w in (400, 500, 600, 700, 800)]
    + [('Poppins', 'poppins-latin-500-italic', 500, 'italic')]
    + [('IBM Plex Mono', f'ibm-plex-mono-latin-{w}-normal', w, 'normal') for w in (400, 500, 600)])
FAVICON = "data:image/svg+xml," + "%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 30 34'%3E%3Cellipse cx='15' cy='29' rx='14' ry='4.6' fill='%23EDE9E5'/%3E%3Cellipse cx='15' cy='20.5' rx='10.5' ry='4' fill='%23EDE9E5'/%3E%3Cellipse cx='15' cy='12.6' rx='7.4' ry='3.4' fill='%23EDE9E5'/%3E%3Cellipse cx='15' cy='5.4' rx='4.6' ry='3' fill='%23FC693A'/%3E%3C/svg%3E"

def english(tpl):
    for fr, en in i18n_en.PAIRS:
        if fr not in tpl:
            sys.exit(f'i18n_en.py: French string not found in template.html, update the pair:\n  {fr[:120]!r}')
        tpl = tpl.replace(fr, en)
    return tpl

def page(lang, tpl, data, ref, site):
    code, title, desc = META[lang]
    other = 'en' if lang == 'fr' else 'fr'
    head_end = tpl.index('<style>')
    body = tpl[head_end:]
    body = body.replace('<style>', '<style>\n' + FONTS, 1)
    body = (body.replace('/*DATA*/', data, 1).replace('/*REF*/null', json.dumps(ref, ensure_ascii=False).replace('</', '<\\/'), 1)
                .replace('__VITRINE__', SHOWCASE).replace('__OTHERLANG__', f'../{other}/').replace('__ASSETS__', '../assets/'))
    return f'''<!doctype html>
<html lang="{code}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>{title}</title>
<meta name="description" content="{desc}">
<link rel="canonical" href="{site}/{lang}/">
<link rel="alternate" hreflang="fr" href="{site}/fr/"><link rel="alternate" hreflang="en" href="{site}/en/">
<meta property="og:title" content="{title}"><meta property="og:description" content="{desc}">
<meta property="og:image" content="{site}/assets/video/poster.jpg"><meta property="og:url" content="{site}/{lang}/">
<link rel="icon" href="{FAVICON}">
</head>
<body>
{body}
</body>
</html>
'''

def check_version(tpl, changelog):
    """The hero badge names the newest released version of the CHANGELOG
    (docs/release.md step 2): a release cut that forgets the site fails."""
    badge = re.search(r'<b class="m">v([^ <]+) · ', tpl)
    released = re.search(r'^## \[(?!Unreleased\])([^\]]+)\]', changelog, re.M)
    if not badge or not released or badge.group(1) != released.group(1):
        sys.exit(f'template.html: the hero badge says v{badge.group(1) if badge else "?"} but the newest CHANGELOG section is '
                 f'{released.group(1) if released else "?"}: update the site in the release cut (docs/release.md step 2)')


def main():
    a = argparse.ArgumentParser(); a.add_argument('--out', default=str(SRC.parent / 'dist')); a.add_argument('--default', default='fr', choices=['fr', 'en'])
    a.add_argument('--site-url', default=SITE)
    o = a.parse_args(); out = pathlib.Path(o.out); site = o.site_url.rstrip('/')
    root = str(SRC.parent.parent)
    ref = gen_ref.reference(root)
    tpl = (SRC / 'template.html').read_text()
    check_version(tpl, (SRC.parent.parent / 'CHANGELOG.md').read_text())
    # the head of the template only serves the claude.ai previews: drop its font links
    shutil.rmtree(out, ignore_errors=True); (out / 'fr').mkdir(parents=True); (out / 'en').mkdir()
    (out / 'fr' / 'index.html').write_text(page('fr', tpl, (SRC / 'data.fr.js').read_text(), ref, site))
    (out / 'en' / 'index.html').write_text(page('en', english(tpl), (SRC / 'data.en.js').read_text(), ref, site))
    d = o.default
    # The root redirects relatively; 404.html is served at any missing path,
    # so it names the default language by its absolute address (the site may
    # sit under a path prefix, as on github.io).
    for name, target in (('index.html', f'{d}/'), ('404.html', f'{site}/{d}/')):
        (out / name).write_text(f'<!doctype html><html lang="{d}"><head><meta charset="utf-8"><title>TakeCare Agents</title>'
                                f'<meta http-equiv="refresh" content="0; url={target}"><link rel="canonical" href="{site}/{d}/">'
                                f'<script>location.replace("{target}"+location.hash)</script></head><body><a href="{target}">TakeCare Agents</a></body></html>\n')
    shutil.copytree(SRC.parent / 'assets', out / 'assets')
    (out / '.nojekyll').write_text('')
    print(f'built {out} (default {d}, reference from {ref["commit"]})')

if __name__ == '__main__':
    main()
