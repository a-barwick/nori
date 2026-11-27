import Foundation

extension NoriStore {
    public func bootstrapIfNeeded() async {
        guard !hasBootstrapped else { return }
        hasBootstrapped = true
        isCheckingSavedSession = true
        let generation = sessionGeneration
        let savedSession: AuthSession?
        if let devLocalEmail {
            savedSession = AuthSession(idToken: "", refreshToken: nil, expiresAt: .distantFuture, email: devLocalEmail)
        } else {
            savedSession = await authProvider.loadSession()
        }
        isCheckingSavedSession = false
        guard generation == sessionGeneration else { return }
        guard let savedSession else {
            authState = .signedOut
            connectionState = .offline
            return
        }

        authState = .signedIn(email: savedSession.email)
        isLoading = true
        connectionState = .connecting
        defer { if generation == sessionGeneration { isLoading = false } }

        do {
            let initialSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applySession(initialSession)
            lastError = nil
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            guard generation == sessionGeneration else { return }
            connectionState = .reconnecting
            lastError = error.localizedDescription
        }
        startChatSession()
    }

    public func beginGoogleSignIn(callbackURL: URL, pkce: GoogleOAuthPKCE) async throws -> GoogleSignInResult {
        authError = nil
        let code = try pkce.extractAuthorizationCode(from: callbackURL)
        let exchangedSession = try await pkce.exchangeCode(code)
        let name = GoogleOAuthPKCE.extractNameFromJWT(exchangedSession.idToken)
        let outcome = try await transport.resolveUser(
            idToken: exchangedSession.idToken,
            email: exchangedSession.email,
            name: name
        )
        switch outcome {
        case .resolved:
            await completeSignedInSession(exchangedSession)
            return .signedIn
        case .inviteRequired:
            return .inviteRequired(exchangedSession)
        case .forbidden(let message):
            authError = message
            return .forbidden(message)
        }
    }

    public func redeemInvite(code: String, for authSession: AuthSession) async throws {
        let name = GoogleOAuthPKCE.extractNameFromJWT(authSession.idToken)
        _ = try await transport.redeemInvite(
            idToken: authSession.idToken,
            inviteCode: code,
            email: authSession.email,
            name: name
        )
        await completeSignedInSession(authSession)
    }

    public func completeGoogleSignIn(callbackURL: URL, pkce: GoogleOAuthPKCE) async {
        sessionGeneration &+= 1
        let generation = sessionGeneration
        clearUserState()
        authError = nil
        authState = .signingIn
        await clearTransportIdentity()
        guard generation == sessionGeneration else { return }
        do {
            let code = try pkce.extractAuthorizationCode(from: callbackURL)
            let exchangedSession = try await pkce.exchangeCode(code)
            guard generation == sessionGeneration else { return }
            await finishSignedInSession(exchangedSession, generation: generation)
        } catch {
            guard generation == sessionGeneration else { return }
            await failSignIn(error, generation: generation)
        }
    }

    public func completeSignedInSession(_ authSession: AuthSession) async {
        sessionGeneration &+= 1
        let generation = sessionGeneration
        clearUserState()
        authError = nil
        authState = .signingIn
        await clearTransportIdentity()
        guard generation == sessionGeneration else { return }
        await finishSignedInSession(authSession, generation: generation)
    }

    private func finishSignedInSession(_ authSession: AuthSession, generation: UInt64) async {
        do {
            await authProvider.saveSession(authSession)
            guard generation == sessionGeneration else { return }
            await clearTransportIdentity()
            guard generation == sessionGeneration else { return }
            let initialSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration else { return }
            applySession(initialSession)
            authState = .signedIn(email: authSession.email)
            connectionState = .connected
            lastError = nil
            NoriLogger.auth.info("auth.signed_in", breadcrumb: true)
            startChatSession()
        } catch {
            guard generation == sessionGeneration else { return }
            await failSignIn(error, generation: generation)
        }
    }

    private func failSignIn(_ error: Error, generation: UInt64) async {
        await authProvider.clearSession()
        guard generation == sessionGeneration else { return }
        await clearTransportIdentity()
        guard generation == sessionGeneration else { return }
        authState = .signedOut
        connectionState = .offline
        if let apiErr = error as? NoriAPIError, apiErr.isAuthError {
            authError = "This Google account is not authorized to access Nori."
            NoriLogger.auth.warning("auth.unauthorized", attributes: ["http.status_code": String(apiErr.statusCode)])
        } else {
            authError = error.localizedDescription
            let tags = NoriErrorDiagnostics.tags(for: error)
            NoriLogger.auth.error("auth.sign_in_failed", attributes: tags)
            NoriTelemetryRuntime.shared.captureError(error, category: "auth.sign_in", tags: tags)
        }
    }

    public func signOut() async {
        sessionGeneration &+= 1
        accountDeletePending = false
        clearUserState()
        NoriLogger.auth.info("auth.signed_out", breadcrumb: true)
        await clearTransportIdentity()
        await authProvider.clearSession()
    }

    public func completeOnboarding(
        name: String,
        replyStyle: String,
        focus: [String],
        workingOn: String,
        saveAboutYou: Bool
    ) async throws {
        let generation = sessionGeneration
        var firstFailure: Error?

        if saveAboutYou {
            var seeds: [(section: String, text: String)] = []
            let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
            let trimmedStyle = replyStyle.trimmingCharacters(in: .whitespacesAndNewlines)
            let trimmedWorking = workingOn.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmedName.isEmpty {
                seeds.append(("Preferences", "The user's name is \(trimmedName)."))
            }
            if !trimmedStyle.isEmpty {
                seeds.append(("Preferences", "Prefers \(trimmedStyle.lowercased()) replies."))
            }
            if !focus.isEmpty {
                let joined = focus.map { $0.lowercased() }.joined(separator: ", ")
                seeds.append(("Context", "Uses Nori for \(joined)."))
            }
            if !trimmedWorking.isEmpty {
                seeds.append(("Context", "Currently working on: \(trimmedWorking)"))
            }
            for seed in seeds {
                let factKey = "\(seed.section):\(seed.text)"
                if onboardingSavedFactKeys.contains(factKey) { continue }
                do {
                    let receipt = try await transport.createMemory(
                        text: seed.text,
                        section: seed.section,
                        about: nil,
                        pinned: false,
                        requestID: UUID().uuidString.lowercased()
                    )
                    guard generation == sessionGeneration else { return }
                    onboardingSavedFactKeys.insert(factKey)
                    if !coreMemories.contains(where: { $0.id == receipt.resource.id }) {
                        coreMemories.insert(receipt.resource, at: 0)
                    }
                } catch {
                    guard generation == sessionGeneration else { return }
                    if await handleAuthFailureIfNeeded(error) { return }
                    if firstFailure == nil {
                        firstFailure = error
                    }
                }
            }
        }

        if !onboardingSavedPreferences {
            var nextPrefs = preferences
            nextPrefs.timezone = TimeZone.current.identifier
            nextPrefs.onboardingCompletedAt = ISO8601DateFormatter().string(from: Date())
            do {
                let receipt = try await transport.savePreferences(
                    nextPrefs,
                    requestID: UUID().uuidString.lowercased()
                )
                guard generation == sessionGeneration else { return }
                appliedPreferencesRead = nextPreferencesRead()
                preferences = receipt.resource
                onboardingSavedPreferences = true
            } catch {
                guard generation == sessionGeneration else { return }
                if await handleAuthFailureIfNeeded(error) { return }
                if firstFailure == nil {
                    firstFailure = error
                }
            }
        }

        if let firstFailure {
            throw firstFailure
        }

        onboardingSavedFactKeys.removeAll()
        onboardingSavedPreferences = false
        if let key = onboardingDoneKey {
            UserDefaults.standard.set(true, forKey: key)
        }
        isShowingOnboardingReplay = false
    }

    public func deleteAccount(confirmation: String, resumePending: Bool = false) async throws {
        guard !isDeletingAccount else {
            throw NoriAPIError(message: "Account deletion is already in progress.")
        }
        isDeletingAccount = true
        defer { isDeletingAccount = false }
        let onboardingKey = onboardingDoneKey
        let observedKey = observedResetKey
        let legacyResetKey = authState.email.map { "nori.data-reset.completion." + $0.lowercased() }
        do {
            try await transport.deleteAccount(
                confirmation: confirmation.trimmingCharacters(in: .whitespacesAndNewlines),
                resumePending: resumePending
            )
        } catch {
            if let apiError = error as? NoriAPIError, apiError.code == "account_delete_pending" {
                accountDeletePending = true
            }
            throw error
        }
        if let key = onboardingKey { UserDefaults.standard.removeObject(forKey: key) }
        if let key = observedKey { UserDefaults.standard.removeObject(forKey: key) }
        if let legacyResetKey { UserDefaults.standard.removeObject(forKey: legacyResetKey) }
        accountDeletePending = false
        accountDeletedNotice = "Your account has been deleted."
        await signOut()
    }

    public func resetUserData(confirmation: String) async throws {
        try await performDataReset(confirmation: confirmation, resumePending: false)
    }

    public func finishPendingDataReset() async throws {
        try await performDataReset(confirmation: "RESET", resumePending: true)
    }

    private func performDataReset(confirmation: String, resumePending: Bool) async throws {
        guard !isResettingData else { throw NoriAPIError(message: "A reset is already in progress.") }
        isResettingData = true
        let duration = resetMinimumDuration
        let minimumDisplay = Task.detached {
            try? await Task.sleep(for: duration)
        }
        defer { isResettingData = false }
        do {
            try await executeDataReset(confirmation: confirmation, resumePending: resumePending)
            _ = await minimumDisplay.value
        } catch {
            _ = await minimumDisplay.value
            throw error
        }
    }

    private func executeDataReset(confirmation: String, resumePending: Bool) async throws {
        let generation = sessionGeneration
        let resetGeneration = dataResetGeneration
        let signedInState = authState
        let previousResetAt = preferences.dataResetAt
        let previousResetKnown = hasLoadedPreferences
        do {
            if resumePending {
                try await transport.resumePendingDataReset()
            } else {
                try await transport.resetUserData(confirmation: confirmation)
            }
        } catch {
            guard signedInState == authState else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.code == "data_reset_pending" {
                enterDataResetRecovery(baseline: previousResetAt, baselineKnown: previousResetKnown, confirmed: true)
            }
            let latest = try? await refreshDataResetStatus()
            guard signedInState == authState else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.code == "data_reset_not_pending", latest != nil {
                return
            }
            if let latest, latest.dataResetPending != true,
               previousResetKnown, let resetAt = latest.dataResetAt, !resetAt.isEmpty,
               resetAt != previousResetAt {
                clearLocalDataForReset()
                await signOut()
                return
            }
            if latest == nil {
                enterDataResetRecovery(baseline: previousResetAt, baselineKnown: previousResetKnown, confirmed: false)
            }
            throw error
        }
        guard generation == sessionGeneration else {
            if signedInState == authState && resetGeneration != dataResetGeneration {
                dataResetRecoveryRequired = false
                clearLocalDataForReset()
                await signOut()
                return
            }
            throw CancellationError()
        }
        guard authState.isSignedIn else { throw CancellationError() }

        clearLocalDataForReset()
        await signOut()
    }

    func enterDataResetRecovery(baseline: String?, baselineKnown: Bool, confirmed: Bool) {
        guard authState.isSignedIn else { return }
        if dataResetRecoveryRequired && (!confirmed || resetRecoveryHasClearedData) {
            if !resetRecoveryBaselineKnown && baselineKnown {
                resetRecoveryBaseline = baseline
                resetRecoveryBaselineKnown = true
            }
            return
        }
        let savedBaseline = resetRecoveryBaselineKnown ? resetRecoveryBaseline : baseline
        let savedBaselineKnown = resetRecoveryBaselineKnown || baselineKnown
        if confirmed {
            clearLocalDataForReset()
        } else {
            sessionGeneration &+= 1
            stopEventStream()
            chatContextTask?.cancel()
            chatContextTask = nil
        }
        resetRecoveryBaseline = savedBaseline
        resetRecoveryBaselineKnown = savedBaselineKnown
        resetRecoveryHasClearedData = confirmed
        dataResetRecoveryRequired = true
        preferences.dataResetPending = true
        hasLoadedPreferences = true
        hasLoadedSnapshot = true
        startChatSession()
    }

    @discardableResult
    public func refreshDataResetStatus() async throws -> Preferences {
        let generation = sessionGeneration
        let readID = nextPreferencesRead()
        let latest = try await transport.fetchPreferences()
        guard generation == sessionGeneration else { throw CancellationError() }
        applyPreferences(latest, readID: readID)
        return latest
    }

    func clearLocalDataForReset() {
        let signedInState = authState
        let currentConnectionState = connectionState
        if let key = onboardingDoneKey { UserDefaults.standard.removeObject(forKey: key) }
        if case .signedIn(let email) = signedInState {
            UserDefaults.standard.removeObject(forKey: "nori.diagnostics.modelSelection.v1." + email.lowercased())
        }
        UserDefaults.standard.removeObject(forKey: "nori.appearance")
        dataResetGeneration &+= 1
        sessionGeneration &+= 1
        clearUserState(preserveIdentity: true)
        authState = signedInState
        connectionState = currentConnectionState
    }

    func nextPreferencesRead() -> UInt64 {
        preferencesReadSequence &+= 1
        return preferencesReadSequence
    }

    @discardableResult
    func applyPreferences(_ latest: Preferences, readID: UInt64) -> Bool {
        guard readID >= appliedPreferencesRead else { return true }
        appliedPreferencesRead = readID
        if latest.dataResetPending == true {
            enterDataResetRecovery(baseline: latest.dataResetAt, baselineKnown: true, confirmed: true)
            preferences = latest
            hasLoadedPreferences = true
            hasLoadedSnapshot = true
            return true
        }
        let wasRecovering = dataResetRecoveryRequired
        let recovered = wasRecovering && resetRecoveryBaselineKnown &&
            latest.dataResetAt != nil && latest.dataResetAt != "" && latest.dataResetAt != resetRecoveryBaseline
        if recovered && !resetRecoveryHasClearedData {
            clearLocalDataForReset()
        }
        var detectedReset = false
        if let resetAt = latest.dataResetAt, !resetAt.isEmpty,
           let key = observedResetKey,
           UserDefaults.standard.string(forKey: key) != resetAt {
            if !isAwaitingResetMarker && !wasRecovering {
                clearLocalDataForReset()
                detectedReset = true
            }
            UserDefaults.standard.set(resetAt, forKey: key)
        }
        isAwaitingResetMarker = false
        if wasRecovering {
            dataResetRecoveryRequired = false
            resetRecoveryBaseline = nil
            resetRecoveryBaselineKnown = false
            resetRecoveryHasClearedData = false
            if recovered {
                postResetTask?.cancel()
                postResetTask = Task { [weak self] in
                    await self?.signOut()
                }
                return true
            }
            detectedReset = true
            dataResetStatusRequired = true
        }
        preferences = latest
        hasLoadedPreferences = true
        if detectedReset {
            hasLoadedSnapshot = true
            startChatSession()
            postResetTask?.cancel()
            postResetTask = Task { [weak self] in
                await self?.refreshAll()
            }
        }
        return detectedReset
    }
}
