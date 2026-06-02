import SwiftUI
import Foundation

/// Fetches a Markdown document over HTTPS and renders it inline — used by
/// Help to show the live iOS client guide (Android renders the same docs
/// via Markwon, keeping help current without an app update). Lightweight
/// block renderer: headings, paragraphs, bullets, fenced code, and pipe
/// tables; inline emphasis/links via AttributedString.
struct MarkdownDocView: View {
    let url: URL
    let title: String

    @State private var blocks: [MDBlock] = []
    @State private var loading = true
    @State private var failed = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                if loading {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                } else if failed {
                    Text("Couldn't load the guide. Check your connection.")
                        .foregroundStyle(.secondary)
                    Link("Open in browser", destination: url)
                } else {
                    ForEach(blocks) { $0.view }
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        loading = true; failed = false
        defer { loading = false }
        do {
            let (data, resp) = try await URLSession.shared.data(from: url)
            guard let http = resp as? HTTPURLResponse, http.statusCode == 200,
                  let text = String(data: data, encoding: .utf8) else {
                failed = true; return
            }
            blocks = MarkdownParser.parse(text)
        } catch {
            failed = true
        }
    }
}

/// One rendered Markdown block.
struct MDBlock: Identifiable {
    let id = UUID()
    enum Kind {
        case heading(level: Int, text: String)
        case paragraph(String)
        case bullet(String)
        case code(String)
        case table(rows: [[String]])
    }
    let kind: Kind

    @ViewBuilder var view: some View {
        switch kind {
        case .heading(let level, let text):
            Text(text)
                .font(level <= 1 ? .title2.bold() : (level == 2 ? .title3.bold() : .headline))
                .padding(.top, 6)
        case .paragraph(let s):
            Text(MDBlock.inline(s))
                .font(.system(size: 15))
                .fixedSize(horizontal: false, vertical: true)
        case .bullet(let s):
            HStack(alignment: .top, spacing: 8) {
                Text("•")
                Text(MDBlock.inline(s)).font(.system(size: 15))
            }
            .fixedSize(horizontal: false, vertical: true)
        case .code(let s):
            Text(s)
                .font(.system(size: 12, design: .monospaced))
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color.gray.opacity(0.12)))
        case .table(let rows):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(rows.enumerated()), id: \.offset) { idx, row in
                    HStack(alignment: .top, spacing: 8) {
                        ForEach(Array(row.enumerated()), id: \.offset) { _, cell in
                            Text(MDBlock.inline(cell))
                                .font(.system(size: 13))
                                .fontWeight(idx == 0 ? .semibold : .regular)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    if idx == 0 { Divider() }
                }
            }
            .padding(8)
            .background(RoundedRectangle(cornerRadius: 8).fill(Color.gray.opacity(0.06)))
        }
    }

    /// Inline emphasis/links only (block syntax handled by the parser).
    static func inline(_ s: String) -> AttributedString {
        (try? AttributedString(
            markdown: s,
            options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        )) ?? AttributedString(s)
    }
}

enum MarkdownParser {
    static func parse(_ text: String) -> [MDBlock] {
        var blocks: [MDBlock] = []
        let lines = text.components(separatedBy: "\n")
        var i = 0
        var paragraph: [String] = []
        func flushPara() {
            if !paragraph.isEmpty {
                blocks.append(MDBlock(kind: .paragraph(paragraph.joined(separator: " "))))
                paragraph = []
            }
        }
        while i < lines.count {
            let line = lines[i].trimmingCharacters(in: .whitespaces)
            if line.isEmpty { flushPara(); i += 1; continue }

            // Fenced code block.
            if line.hasPrefix("```") {
                flushPara()
                var code: [String] = []
                i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                    code.append(lines[i]); i += 1
                }
                i += 1 // closing fence
                blocks.append(MDBlock(kind: .code(code.joined(separator: "\n"))))
                continue
            }

            // Heading.
            if line.hasPrefix("#") {
                flushPara()
                let level = line.prefix(while: { $0 == "#" }).count
                let txt = line.drop(while: { $0 == "#" }).trimmingCharacters(in: .whitespaces)
                blocks.append(MDBlock(kind: .heading(level: level, text: txt)))
                i += 1; continue
            }

            // Pipe table.
            if line.hasPrefix("|") {
                flushPara()
                var rows: [[String]] = []
                while i < lines.count, lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("|") {
                    let cells = lines[i].trimmingCharacters(in: .whitespaces)
                        .trimmingCharacters(in: CharacterSet(charactersIn: "|"))
                        .components(separatedBy: "|")
                        .map { $0.trimmingCharacters(in: .whitespaces) }
                    let isSeparator = cells.allSatisfy { !$0.isEmpty && $0.allSatisfy { $0 == "-" || $0 == ":" } }
                    if !isSeparator { rows.append(cells) }
                    i += 1
                }
                if !rows.isEmpty { blocks.append(MDBlock(kind: .table(rows: rows))) }
                continue
            }

            // Horizontal rule / front-matter fence — skip.
            if line == "---" || line == "***" { flushPara(); i += 1; continue }

            // Bullet.
            if line.hasPrefix("- ") || line.hasPrefix("* ") {
                flushPara()
                blocks.append(MDBlock(kind: .bullet(String(line.dropFirst(2)))))
                i += 1; continue
            }

            // Blockquote marker — render the text as a paragraph.
            if line.hasPrefix(">") {
                paragraph.append(line.dropFirst().trimmingCharacters(in: .whitespaces))
                i += 1; continue
            }

            paragraph.append(line)
            i += 1
        }
        flushPara()
        return blocks
    }
}
