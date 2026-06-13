import SwiftUI

/// Settings/Rules building blocks that match the tvOS design template's
/// component vocabulary (`.setrow`, `.tg` toggle pill, `.seg` segmented control,
/// `.setb` action buttons) — instead of raw tvOS Picker/Toggle/TextField, which
/// are a 10-foot usability mess. Rows are `.card` buttons so the focus
/// lift/teal-glow comes for free and matches `.setrow.focus`.

/// The template's `.tg` — a 60×34 pill with a sliding white knob.
struct TVTogglePill: View {
    let on: Bool
    var body: some View {
        Capsule()
            .fill(on ? TVColor.teal : TVColor.surfaceVariant)
            .frame(width: 62, height: 34)
            .overlay(Capsule().stroke(on ? TVColor.teal : TVColor.outline, lineWidth: 1))
            .overlay(alignment: on ? .trailing : .leading) {
                Circle().fill(.white).frame(width: 26, height: 26)
                    .shadow(color: .black.opacity(0.4), radius: 2, y: 1)
                    .padding(.horizontal, 4)
            }
            .animation(.easeInOut(duration: 0.18), value: on)
    }
}

/// `.setrow` with a toggle — the whole row is the focusable control.
struct TVToggleRow: View {
    let title: String
    var description: String? = nil
    @Binding var isOn: Bool
    var onChange: (Bool) -> Void = { _ in }

    var body: some View {
        Button {
            isOn.toggle(); onChange(isOn)
        } label: {
            HStack(spacing: 24) {
                rowText(title, description)
                Spacer(minLength: 12)
                TVTogglePill(on: isOn)
            }
            .padding(.vertical, 22).padding(.horizontal, 28)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.card)
    }
}

/// `.setrow` shell: title + description on the left, custom trailing content on
/// the right. Non-focusable container (its trailing controls take focus).
struct TVSetRow<Trailing: View>: View {
    let title: String
    var description: String? = nil
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(alignment: .center, spacing: 24) {
            rowText(title, description)
            Spacer(minLength: 12)
            trailing()
        }
        .padding(.vertical, 22).padding(.horizontal, 28)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(TVColor.outline, lineWidth: 1))
    }
}

/// A titled block (title + description above, custom content below) — for
/// controls too wide to sit inline (segmented rows, input + presets).
struct TVSettingsBlock<Content: View>: View {
    let title: String
    var description: String? = nil
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            rowText(title, description)
            content()
        }
        .padding(.vertical, 22).padding(.horizontal, 28)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(TVColor.outline, lineWidth: 1))
    }
}

/// The template's `.seg` — equal-width segments, selected = teal.
struct TVSegmented<T: Hashable>: View {
    let options: [(value: T, label: String)]
    @Binding var selection: T
    var onChange: (T) -> Void = { _ in }

    var body: some View {
        HStack(spacing: 12) {
            ForEach(options.indices, id: \.self) { i in
                let opt = options[i]
                let on = opt.value == selection
                Button {
                    selection = opt.value; onChange(opt.value)
                } label: {
                    Text(opt.label).font(TVFont.sans(19, .semibold))
                        .foregroundStyle(on ? TVColor.teal : TVColor.onSurfaceVariant)
                        .lineLimit(1).minimumScaleFactor(0.55)
                        .frame(maxWidth: .infinity).padding(.vertical, 15)
                }
                .buttonStyle(.card)
            }
        }
    }
}

/// `.setb`-style compact action button (teal text on a teal-tinted card).
struct TVActionButton: View {
    let title: String
    var icon: String? = nil
    let action: () -> Void
    var role: ButtonRole? = nil

    var body: some View {
        Button(role: role, action: action) {
            HStack(spacing: 9) {
                if let icon { Image(systemName: icon).font(.system(size: 19, weight: .semibold)) }
                Text(title).font(TVFont.sans(19, .semibold)).lineLimit(1).minimumScaleFactor(0.6)
            }
            .foregroundStyle(role == .destructive ? TVColor.error : TVColor.teal)
            .padding(.vertical, 13).padding(.horizontal, 22)
        }
        .buttonStyle(.card)
    }
}

/// Shared left-hand title + description column used by the rows above.
@ViewBuilder private func rowText(_ title: String, _ description: String?) -> some View {
    VStack(alignment: .leading, spacing: 6) {
        Text(title).font(TVFont.sans(24, .semibold)).foregroundStyle(TVColor.onSurface)
        if let d = description, !d.isEmpty {
            Text(d).font(TVFont.sans(17)).foregroundStyle(TVColor.onSurfaceVariant)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
