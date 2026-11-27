import Foundation

extension NoriStore {
    public func refreshPermissionsAndPreferences() async {
        let generation = sessionGeneration
        let preferencesReadID = nextPreferencesRead()
        let activeTransport = transport
        async let permsResult: Result<[PermissionRule], Error> = {
            do { return .success(try await activeTransport.fetchPermissions()) }
            catch { return .failure(error) }
        }()
        async let prefsResult: Result<Preferences, Error> = {
            do { return .success(try await activeTransport.fetchPreferences()) }
            catch { return .failure(error) }
        }()

        let (permsRes, prefsRes) = await (permsResult, prefsResult)
        guard generation == sessionGeneration, !Task.isCancelled else { return }
        if case .failure(let err) = permsRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = prefsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let perms) = permsRes {
            self.permissions = perms
        }
        if case .success(let prefs) = prefsRes {
            applyPreferences(prefs, readID: preferencesReadID)
        }
    }

    public func createPermissionRule(_ rule: PermissionRuleEditable) async -> Bool {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.createPermissionRule(rule, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return false }
            if let idx = permissions.firstIndex(where: { $0.id == receipt.resource.id }) {
                permissions[idx] = receipt.resource
            } else {
                permissions.insert(receipt.resource, at: 0)
            }
            showToast("Permission rule saved")
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            _ = await handleOperationError(error)
            return false
        }
    }

    public func revokePermissionRule(id: String) async {
        let generation = sessionGeneration
        guard let rule = permissions.first(where: { $0.id == id }) else { return }
        do {
            _ = try await transport.revokePermissionRule(
                id: id,
                expectedRevision: rule.revision,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            permissions.removeAll { $0.id == id }
            showToast("Permission rule removed")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func refreshPermissionsThrowing() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchPermissions()
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            permissions = latest
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshPreferencesThrowing() async throws {
        let generation = sessionGeneration
        let preferencesReadID = nextPreferencesRead()
        do {
            let latest = try await transport.fetchPreferences()
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            applyPreferences(latest, readID: preferencesReadID)
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func savePreferences(_ updated: Preferences) async throws {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.savePreferences(updated, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { throw CancellationError() }
            appliedPreferencesRead = nextPreferencesRead()
            preferences = receipt.resource
            showToast("Preferences saved")
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func updatePreferences(_ updated: Preferences) async {
        let generation = sessionGeneration
        do {
            try await savePreferences(updated)
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func refreshModelKeys() async {
        let generation = sessionGeneration
        do {
            try await refreshModelKeysThrowing()
            if modelSettings == nil && modelKeys.byokRequired && !hasConfiguredModelKey {
                try? await refreshModelSettings()
            }
        } catch is CancellationError {
            return
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
        }
    }

    public func fetchAPICosts(days: Int) async throws -> APICostResponse {
        let generation = sessionGeneration
        do {
            let result = try await transport.fetchAPICosts(days: days)
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            return result
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshModelSettings() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchModelSettings()
            guard generation == sessionGeneration else { throw CancellationError() }
            modelSettings = latest
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshHomeConnectors() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchHomeConnectors()
            guard generation == sessionGeneration else { throw CancellationError() }
            homeConnectors = latest
        } catch let error as NoriAPIError where error.statusCode == 404 {
            guard generation == sessionGeneration else { throw CancellationError() }
            homeConnectors = nil
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func lookupHomePairing(_ code: String) async throws -> HomePairing {
        let generation = sessionGeneration
        do {
            let result = try await transport.lookupHomePairing(code)
            guard generation == sessionGeneration else { throw CancellationError() }
            return result
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func approveHomePairing(_ code: String) async throws {
        let generation = sessionGeneration
        do {
            try await transport.approveHomePairing(code)
            guard generation == sessionGeneration else { throw CancellationError() }
            try? await refreshHomeConnectors()
            try? await refreshModelSettings()
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func testHomeConnector(_ id: String) async throws {
        let generation = sessionGeneration
        do {
            let result = try await transport.testHomeConnector(id)
            guard generation == sessionGeneration else { throw CancellationError() }
            guard result.status == "ok" else { throw NoriAPIError(message: "Connection test did not complete.") }
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func revokeHomeConnector(_ id: String) async throws {
        let generation = sessionGeneration
        do {
            try await transport.revokeHomeConnector(id)
            guard generation == sessionGeneration else { throw CancellationError() }
            try? await refreshHomeConnectors()
            try? await refreshModelSettings()
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func saveModelSettings(_ selection: ModelSelection, expectedRevision: Int? = nil) async throws {
        let generation = sessionGeneration
        guard let settings = modelSettings else {
            throw NoriAPIError(message: "Reload model settings and try again.")
        }
        do {
            let request = SaveModelSettingsRequest(
                selection: selection,
                expectedRevision: expectedRevision ?? settings.revision
            )
            let updated = try await transport.saveModelSettings(request)
            guard generation == sessionGeneration else { throw CancellationError() }
            modelSettings = updated
            modelKeyError = nil
            showToast("Models saved")
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.isRevisionConflict, apiError.code != "data_reset_pending" {
                try? await refreshModelSettings()
            }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshModelKeysThrowing() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchModelKeys()
            guard generation == sessionGeneration else { return }
            modelKeys = latest
        } catch {
            guard generation == sessionGeneration else { return }
            if await handleAuthFailureIfNeeded(error) {
                return
            }
            throw error
        }
    }

    @discardableResult
    public func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        let generation = sessionGeneration
        do {
            let updated = try await transport.saveModelKey(provider: provider, apiKey: apiKey)
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertModelKeyProvider(updated)
            modelKeyError = nil
            showToast("\(updated.label) key saved")
            return updated
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if await handleAuthFailureIfNeeded(error) {
                throw error
            }
            lastError = error.localizedDescription
            throw error
        }
    }

    @discardableResult
    public func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        let generation = sessionGeneration
        do {
            let updated = try await transport.deleteModelKey(provider: provider)
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertModelKeyProvider(updated)
            showToast("\(updated.label) key removed")
            return updated
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if await handleAuthFailureIfNeeded(error) {
                throw error
            }
            lastError = error.localizedDescription
            throw error
        }
    }

    private func upsertModelKeyProvider(_ updated: ModelKeyProvider) {
        if let idx = modelKeys.providers.firstIndex(where: { $0.provider == updated.provider }) {
            modelKeys.providers[idx] = updated
        } else {
            modelKeys.providers.append(updated)
        }
    }
}
