/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const desktopRoot = fileURLToPath(new URL('../', import.meta.url))
const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url))
const projectVersion = (await readFile(`${repositoryRoot}VERSION`, 'utf8')).trim()
const semverVersion = /^\d+\.\d+$/.test(projectVersion)
  ? `${projectVersion}.0`
  : projectVersion
const packageJson = JSON.parse(await readFile(`${desktopRoot}package.json`, 'utf8'))
const cargoToml = await readFile(`${desktopRoot}src-tauri/Cargo.toml`, 'utf8')
const cargoVersion = cargoToml.match(/^version\s*=\s*"([^"]+)"/m)?.[1]

if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(semverVersion)) {
  throw new Error(`VERSION must map to SemVer, received ${JSON.stringify(projectVersion)}`)
}
if (packageJson.version !== semverVersion || cargoVersion !== semverVersion) {
  throw new Error(
    `VERSION ${projectVersion} maps to ${semverVersion}, but desktop manifests are ` +
      `npm=${packageJson.version} cargo=${cargoVersion}`,
  )
}

console.log(`desktop version ${semverVersion} matches VERSION ${projectVersion}`)
