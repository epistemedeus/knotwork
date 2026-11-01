import '@knadh/oat/oat.min.css'
import '@knadh/oat/oat.min.js'
import { File, FileDiff, canUseWASM, parsePatchFiles } from '@pierre/diffs'
import { FileTree } from '@pierre/trees'
import './styles.css'

const highlighter = canUseWASM() ? 'highlights' : 'shiki-js'
const diffTheme = { dark: 'pierre-dark', light: 'pierre-light' } as const

type TreeResponse = { ref: string; paths: string[] }
type BlobResponse = {
  path: string
  ref: string
  content: string
  binary: boolean
  truncated: boolean
}

function el<T extends HTMLElement>(id: string): T | null {
  const node = document.getElementById(id)
  return node as T | null
}

function statusMessage(target: HTMLElement, message: string, kind: 'muted' | 'error' = 'muted') {
  const p = document.createElement('p')
  p.className = kind === 'error' ? 'error' : 'meta'
  p.textContent = message
  target.replaceChildren(p)
}

function errorText(body: string): string {
  try {
    const parsed = JSON.parse(body) as { error?: string }
    if (parsed.error) return parsed.error
  } catch {
    // Not JSON; fall through to the raw body.
  }
  return body.trim() || 'Request failed'
}

async function readError(res: Response): Promise<string> {
  return errorText(await res.text())
}

function repoPathFromElement(node: HTMLElement): string {
  const repo = node.dataset.repo
  if (!repo) throw new Error('repository path is missing from the page')
  return repo
}

async function initRepoBrowser() {
  const treeHost = el<HTMLElement>('file-tree')
  const view = el<HTMLElement>('file-view')
  if (!treeHost || !view) return

  let repo: string
  try {
    repo = repoPathFromElement(treeHost)
  } catch (err) {
    statusMessage(view, (err as Error).message, 'error')
    return
  }
  const ref = treeHost.dataset.ref || 'HEAD'

  let paths: string[]
  try {
    const res = await fetch(`/api/repos/${repo}/tree?ref=${encodeURIComponent(ref)}`)
    if (!res.ok) throw new Error(await readError(res))
    const data = (await res.json()) as TreeResponse
    paths = data.paths
  } catch (err) {
    statusMessage(treeHost, (err as Error).message, 'error')
    statusMessage(view, 'The repository tree could not be loaded.', 'error')
    return
  }

  if (paths.length === 0) {
    statusMessage(treeHost, 'This repository has no files yet. Push a commit to get started.')
    statusMessage(view, 'Nothing to display.')
    return
  }

  const files = new Set(paths)
  treeHost.replaceChildren()
  let fileView: File | null = null
  let requestId = 0

  const showFile = async (path: string | undefined) => {
    if (!path || !files.has(path)) {
      return
    }
    const current = ++requestId
    statusMessage(view, `Loading ${path}…`)
    try {
      const res = await fetch(
        `/api/repos/${repo}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`,
      )
      if (!res.ok) throw new Error(await readError(res))
      const blob = (await res.json()) as BlobResponse
      if (current !== requestId) return

      view.replaceChildren()
      const stage = document.createElement('div')
      stage.className = 'file-render'
      view.append(stage)

      if (blob.binary) {
        statusMessage(view, `${path} is a binary file and cannot be displayed here.`)
        return
      }
      if (blob.truncated) {
        const notice = document.createElement('p')
        notice.className = 'meta'
        notice.textContent = 'Showing the first 1 MiB of this file.'
        view.prepend(notice)
      }

      fileView?.cleanUp()
      fileView = new File({
        theme: diffTheme,
        overflow: 'scroll',
        preferredHighlighter: highlighter,
      })
      fileView.render({
        file: { name: path, contents: blob.content, cacheKey: `${ref}:${path}` },
        containerWrapper: stage,
      })
    } catch (err) {
      if (current === requestId) statusMessage(view, (err as Error).message, 'error')
    }
  }

  const tree = new FileTree({
    paths,
    search: true,
    initialExpansion: 1,
    onSelectionChange: (selected) => {
      const last = selected[selected.length - 1]
      void showFile(last)
    },
  })
  tree.render({ fileTreeContainer: treeHost })
}

async function initMergeRequest() {
  const host = el<HTMLElement>('diff-view')
  if (!host) return
  let repo: string
  try {
    repo = repoPathFromElement(host)
  } catch (err) {
    statusMessage(host, (err as Error).message, 'error')
    return
  }
  const number = host.dataset.number
  if (!number) {
    statusMessage(host, 'Merge request number is missing from the page.', 'error')
    return
  }

  let patch: string
  try {
    const res = await fetch(`/api/repos/${repo}/merge-requests/${number}/diff`)
    if (!res.ok) throw new Error(await readError(res))
    patch = await res.text()
  } catch (err) {
    statusMessage(host, (err as Error).message, 'error')
    return
  }

  const patches = parsePatchFiles(patch, `${repo}#${number}`)
  const files = patches.flatMap((parsed) => parsed.files)
  host.replaceChildren()
  if (files.length === 0) {
    statusMessage(host, 'This merge request has no changes.')
    return
  }

  const summary = document.createElement('p')
  summary.className = 'meta'
  summary.textContent = `${files.length} changed ${files.length === 1 ? 'file' : 'files'}`
  host.append(summary)

  for (const fileDiff of files) {
    const wrapper = document.createElement('div')
    wrapper.className = 'diff-file'
    host.append(wrapper)
    const instance = new FileDiff({
      theme: diffTheme,
      preferredHighlighter: highlighter,
      diffStyle: 'unified',
    })
    instance.render({ fileDiff, containerWrapper: wrapper })
  }
}

function initPipeline() {
  const log = el<HTMLElement>('pipeline-log')
  if (!log) return
  const pipelineID = log.dataset.pipeline
  if (!pipelineID) return

  const append = (text: string) => {
    const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 24
    log.textContent = `${log.textContent ?? ''}${text}\n`
    if (atBottom) log.scrollTop = log.scrollHeight
  }

  const stream = new EventSource(`/api/pipelines/${encodeURIComponent(pipelineID)}/logs`)
  stream.onmessage = (event) => append(event.data)
  stream.onerror = () => {
    if (stream.readyState === EventSource.CLOSED) append('[knotwork] log stream closed')
  }

  const button = el<HTMLButtonElement>('approve-pipeline')
  if (button) {
    button.addEventListener('click', async () => {
      const repo = button.dataset.repo
      if (!repo) return
      button.disabled = true
      try {
        const res = await fetch(
          `/api/repos/${repo}/pipelines/${encodeURIComponent(pipelineID)}/approve`,
          { method: 'POST' },
        )
        if (!res.ok) throw new Error(await readError(res))
        const status = el<HTMLElement>('pipeline-status')
        if (status) status.textContent = 'queued'
        button.remove()
      } catch (err) {
        button.disabled = false
        append(`[knotwork] approval failed: ${(err as Error).message}`)
      }
    })
  }
}

function init() {
  switch (document.body.dataset.page) {
    case 'repo':
      void initRepoBrowser()
      break
    case 'merge-request':
      void initMergeRequest()
      break
    case 'pipeline':
      initPipeline()
      break
  }
}

init()
