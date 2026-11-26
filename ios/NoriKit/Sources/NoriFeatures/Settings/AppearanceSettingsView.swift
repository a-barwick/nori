import SwiftUI
import NoriDesign

public struct AppearanceSettingsView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Namespace private var appearancePickerNamespace

    public init() {}

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriCard(padding: 14) {
                    VStack(alignment: .leading, spacing: 14) {
                        VStack(alignment: .leading, spacing: 10) {
                            HStack {
                                Spacer(minLength: 32)
                                Text("What do I have tomorrow?")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.onAccent)
                                    .padding(.horizontal, 11)
                                    .padding(.vertical, 7)
                                    .background(NoriColors.brandPrimary, in: Capsule())
                            }

                            HStack(alignment: .top, spacing: 7) {
                                NoriMark(size: 20, state: .idle)
                                    .padding(.top, 1)
                                Text("Two morning meetings and a reminder at noon.")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.ink)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(
                            NoriColors.canvas,
                            in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )

                        HStack(spacing: 4) {
                            ForEach(NoriAppearance.allCases) { option in
                                let isSelected = appearance == option
                                Button {
                                    withAnimation(.spring(response: 0.24, dampingFraction: 0.86)) {
                                        appearanceRawValue = option.rawValue
                                    }
                                } label: {
                                    HStack(spacing: 5) {
                                        Image(systemName: option.systemImage)
                                            .font(.system(size: 12, weight: .semibold))
                                        Text(option.displayLabel)
                                            .font(.system(size: 13, weight: .semibold))
                                    }
                                    .foregroundStyle(isSelected ? NoriColors.ink : NoriColors.ink2)
                                    .frame(maxWidth: .infinity)
                                    .frame(height: 34)
                                    .background {
                                        if isSelected {
                                            Capsule()
                                                .fill(NoriColors.selected)
                                                .overlay(
                                                    Capsule()
                                                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                                                )
                                                .matchedGeometryEffect(id: "noriAppearanceSelection", in: appearancePickerNamespace)
                                        }
                                    }
                                    .contentShape(Capsule())
                                }
                                .buttonStyle(.plain)
                                .accessibilityAddTraits(isSelected ? .isSelected : [])
                            }
                        }
                        .padding(3)
                        .background(NoriColors.elevated, in: Capsule())
                        .overlay(
                            Capsule()
                                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                        )
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Appearance")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }
}
